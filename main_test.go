package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	t.Parallel()

	t.Run("defaults and overrides", func(t *testing.T) {
		t.Parallel()
		cfg, err := ParseConfig(
			"alice",
			"topsecret",
			"",                // threshold (should use default)
			"bob,  carol ,  ", // whitelist
			"1",               // concurrency
		)
		if err != nil {
			t.Fatalf("ParseConfig error: %v", err)
		}
		if cfg.Username != "alice" {
			t.Errorf("Username = %q, want %q", cfg.Username, "alice")
		}
		if cfg.PAT != "topsecret" {
			t.Errorf("PAT = %q, want %q", cfg.PAT, "topsecret")
		}
		if cfg.Threshold != defaultThreshold {
			t.Errorf("Threshold = %d, want %d", cfg.Threshold, defaultThreshold)
		}
		if cfg.MaxConcurrent != 1 {
			t.Errorf("MaxConcurrent = %d, want %d", cfg.MaxConcurrent, 1)
		}
		wantWL := map[string]struct{}{"bob": {}, "carol": {}}
		if !reflect.DeepEqual(cfg.Whitelist, wantWL) {
			t.Errorf("Whitelist = %#v, want %#v", cfg.Whitelist, wantWL)
		}
	})

	t.Run("valid overrides", func(t *testing.T) {
		t.Parallel()
		cfg, err := ParseConfig("u", "t", "123", "x,y", "5")
		if err != nil {
			t.Fatalf("ParseConfig error: %v", err)
		}
		if cfg.Threshold != 123 {
			t.Errorf("Threshold = %d, want %d", cfg.Threshold, 123)
		}
		if cfg.MaxConcurrent != 5 {
			t.Errorf("MaxConcurrent = %d, want %d", cfg.MaxConcurrent, 5)
		}
	})

	t.Run("missing username", func(t *testing.T) {
		t.Parallel()
		_, err := ParseConfig("", "t", "", "", "")
		if err == nil {
			t.Error("expected error for missing username")
		}
	})

	t.Run("missing token", func(t *testing.T) {
		t.Parallel()
		_, err := ParseConfig("u", "", "", "", "")
		if err == nil {
			t.Error("expected error for missing token")
		}
	})
}

func TestParseNextLink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		link   string
		wantEP string
		wantOK bool
	}{
		{
			name:   "empty",
			link:   "",
			wantEP: "",
			wantOK: false,
		},
		{
			name:   "single next",
			link:   `<https://api.github.com/foo?page=2>; rel="next"`,
			wantEP: "/foo?page=2",
			wantOK: true,
		},
		{
			name:   "multiple links picks next",
			link:   `<https://api.github.com/foo?page=2>; rel="next", <https://api.github.com/foo?page=34>; rel="last"`,
			wantEP: "/foo?page=2",
			wantOK: true,
		},
		{
			name:   "no next",
			link:   `<https://api.github.com/foo?page=34>; rel="last"`,
			wantEP: "",
			wantOK: false,
		},
		{
			name:   "malformed url",
			link:   `<::::>; rel="next"`,
			wantEP: "",
			wantOK: false,
		},
		{
			name:   "absolute URL extracts path",
			link:   `<https://example.com/users/u/followers?per_page=100&page=3>; rel="next"`,
			wantEP: "/users/u/followers?per_page=100&page=3",
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ep, ok := parseNextLink(tt.link)
			if ok != tt.wantOK || ep != tt.wantEP {
				t.Errorf("parseNextLink() = (%q, %v), want (%q, %v)", ep, ok, tt.wantEP, tt.wantOK)
			}
		})
	}
}

func TestNewGitHubClient(t *testing.T) {
	t.Parallel()

	gh := NewGitHubClient("mytoken", "myagent")

	if gh.BaseURL != githubBaseURL {
		t.Errorf("BaseURL = %q, want %q", gh.BaseURL, githubBaseURL)
	}
	if gh.Token != "mytoken" {
		t.Errorf("Token = %q, want %q", gh.Token, "mytoken")
	}
	if gh.UserAgent != "myagent" {
		t.Errorf("UserAgent = %q, want %q", gh.UserAgent, "myagent")
	}
	if gh.HTTP == nil {
		t.Fatal("HTTP client is nil")
	}
	if gh.HTTP.Timeout != 10*time.Second {
		t.Errorf("HTTP.Timeout = %v, want %v", gh.HTTP.Timeout, 10*time.Second)
	}
}

func TestNewRequest(t *testing.T) {
	t.Parallel()

	t.Run("sets headers correctly", func(t *testing.T) {
		t.Parallel()
		gh := &GitHubClient{
			BaseURL:   "https://api.example.com",
			Token:     "testtoken",
			UserAgent: "testagent",
		}

		req, err := gh.newRequest(context.Background(), http.MethodGet, "/test", nil)
		if err != nil {
			t.Fatalf("newRequest error: %v", err)
		}

		wantHeaders := map[string]string{
			"Authorization":        "Bearer testtoken",
			"Accept":               "application/vnd.github+json",
			"X-GitHub-Api-Version": apiVersion,
			"User-Agent":           "testagent",
		}
		for header, want := range wantHeaders {
			if got := req.Header.Get(header); got != want {
				t.Errorf("%s = %q, want %q", header, got, want)
			}
		}
	})

	t.Run("invalid URL returns error", func(t *testing.T) {
		t.Parallel()
		gh := &GitHubClient{BaseURL: "://invalid-url"}

		_, err := gh.newRequest(context.Background(), http.MethodGet, "/test", nil)
		if err == nil {
			t.Fatal("expected error for invalid URL")
		}
		if !strings.Contains(err.Error(), "failed to create request") {
			t.Errorf("error = %q, want to contain 'failed to create request'", err.Error())
		}
	})
}

type failingRoundTripper struct {
	err error
}

func (f *failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, f.err
}

func newFailingClient(err error) *http.Client {
	return &http.Client{Transport: &failingRoundTripper{err: err}}
}

func TestDo(t *testing.T) {
	t.Parallel()

	t.Run("network error", func(t *testing.T) {
		t.Parallel()
		gh := &GitHubClient{
			BaseURL: "https://api.example.com",
			HTTP:    newFailingClient(errors.New("connection refused")),
			Token:   "t",
		}

		req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/test", nil)
		_, err := gh.do(req)
		if err == nil {
			t.Fatal("expected error for network failure")
		}
		if !strings.Contains(err.Error(), "failed to do request") {
			t.Errorf("error = %q, want to contain 'failed to do request'", err.Error())
		}
	})

	t.Run("error response truncation", func(t *testing.T) {
		t.Parallel()
		largeBody := strings.Repeat("x", 10000)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(largeBody))
		}))
		t.Cleanup(srv.Close)

		gh := &GitHubClient{
			BaseURL: srv.URL,
			HTTP:    &http.Client{Timeout: 5 * time.Second},
			Token:   "t",
		}

		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/test", nil)
		_, err := gh.do(req)
		if err == nil {
			t.Fatal("expected error for 500 response")
		}
		if len(err.Error()) > 9000 {
			t.Errorf("error message too long (%d chars), should be truncated", len(err.Error()))
		}
	})
}

func TestGetFollowers(t *testing.T) {
	t.Parallel()

	t.Run("pagination", func(t *testing.T) {
		t.Parallel()
		st := &srvState{}
		srv := newTestServer(t, st)
		t.Cleanup(srv.Close)

		basePath := "/users/alice/followers"
		q1 := url.Values{"per_page": {"100"}, "page": {"1"}}
		q2 := url.Values{"per_page": {"100"}, "page": {"2"}}

		st.mu.Lock()
		st.followersPages[basePath+"?"+q1.Encode()] = []User{{Login: "u1"}, {Login: "u2"}}
		st.followersPages[basePath+"?"+q2.Encode()] = []User{{Login: "u3"}}
		st.mu.Unlock()

		gh := newTestClient(srv.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		got, err := gh.GetFollowers(ctx, "alice")
		if err != nil {
			t.Fatalf("GetFollowers error: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("followers len = %d, want 3; got = %#v", len(got), got)
		}
	})

	t.Run("empty list", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("[]"))
		}))
		t.Cleanup(srv.Close)

		gh := newTestClient(srv.URL)
		followers, err := gh.GetFollowers(context.Background(), "alice")
		if err != nil {
			t.Fatalf("GetFollowers error: %v", err)
		}
		if len(followers) != 0 {
			t.Errorf("followers len = %d, want 0", len(followers))
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("not valid json"))
		}))
		t.Cleanup(srv.Close)

		gh := newTestClient(srv.URL)
		_, err := gh.GetFollowers(context.Background(), "alice")
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
		if !strings.Contains(err.Error(), "failed to decode followers") {
			t.Errorf("error = %q, want to contain 'failed to decode followers'", err.Error())
		}
	})

	t.Run("API error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"message": "rate limit exceeded"}`))
		}))
		t.Cleanup(srv.Close)

		gh := newTestClient(srv.URL)
		_, err := gh.GetFollowers(context.Background(), "alice")
		if err == nil {
			t.Fatal("expected error for API error response")
		}
		if !strings.Contains(err.Error(), "failed to get followers page") {
			t.Errorf("error = %q, want to contain 'failed to get followers page'", err.Error())
		}
	})

	t.Run("network error", func(t *testing.T) {
		t.Parallel()
		gh := &GitHubClient{
			BaseURL: "https://api.example.com",
			HTTP:    newFailingClient(errors.New("connection refused")),
			Token:   "t",
		}

		_, err := gh.GetFollowers(context.Background(), "alice")
		if err == nil {
			t.Fatal("expected error for network failure")
		}
	})

	t.Run("invalid base URL", func(t *testing.T) {
		t.Parallel()
		gh := &GitHubClient{
			BaseURL: "://invalid",
			HTTP:    &http.Client{Timeout: 5 * time.Second},
			Token:   "t",
		}

		_, err := gh.GetFollowers(context.Background(), "alice")
		if err == nil {
			t.Fatal("expected error for invalid base URL")
		}
	})
}

func TestGetFollowingCount(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"login": "alice", "following": 42})
		}))
		t.Cleanup(srv.Close)

		gh := newTestClient(srv.URL)
		count, err := gh.GetFollowingCount(context.Background(), "alice")
		if err != nil {
			t.Fatalf("GetFollowingCount error: %v", err)
		}
		if count != 42 {
			t.Errorf("count = %d, want 42", count)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("not valid json"))
		}))
		t.Cleanup(srv.Close)

		gh := newTestClient(srv.URL)
		_, err := gh.GetFollowingCount(context.Background(), "alice")
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
		if !strings.Contains(err.Error(), "failed to decode user") {
			t.Errorf("error = %q, want to contain 'failed to decode user'", err.Error())
		}
	})

	t.Run("network error", func(t *testing.T) {
		t.Parallel()
		gh := &GitHubClient{
			BaseURL: "https://api.example.com",
			HTTP:    newFailingClient(errors.New("connection refused")),
			Token:   "t",
		}

		_, err := gh.GetFollowingCount(context.Background(), "alice")
		if err == nil {
			t.Fatal("expected error for network failure")
		}
		if !strings.Contains(err.Error(), "failed to get user") {
			t.Errorf("error = %q, want to contain 'failed to get user'", err.Error())
		}
	})

	t.Run("invalid base URL", func(t *testing.T) {
		t.Parallel()
		gh := &GitHubClient{
			BaseURL: "://invalid",
			HTTP:    &http.Client{Timeout: 5 * time.Second},
			Token:   "t",
		}

		_, err := gh.GetFollowingCount(context.Background(), "alice")
		if err == nil {
			t.Fatal("expected error for invalid base URL")
		}
	})
}

func TestBlockUser(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		var blocked atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/user/blocks/") {
				blocked.Store(true)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)

		gh := newTestClient(srv.URL)
		err := gh.BlockUser(context.Background(), "spammer")
		if err != nil {
			t.Fatalf("BlockUser error: %v", err)
		}
		if !blocked.Load() {
			t.Error("user was not blocked")
		}
	})

	t.Run("API error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"message": "forbidden"}`))
		}))
		t.Cleanup(srv.Close)

		gh := newTestClient(srv.URL)
		err := gh.BlockUser(context.Background(), "spammer")
		if err == nil {
			t.Fatal("expected error for API error response")
		}
		if !strings.Contains(err.Error(), "failed to block") {
			t.Errorf("error = %q, want to contain 'failed to block'", err.Error())
		}
	})

	t.Run("network error", func(t *testing.T) {
		t.Parallel()
		gh := &GitHubClient{
			BaseURL: "https://api.example.com",
			HTTP:    newFailingClient(errors.New("connection refused")),
			Token:   "t",
		}

		err := gh.BlockUser(context.Background(), "spammer")
		if err == nil {
			t.Fatal("expected error for network failure")
		}
	})

	t.Run("invalid base URL", func(t *testing.T) {
		t.Parallel()
		gh := &GitHubClient{
			BaseURL: "://invalid",
			HTTP:    &http.Client{Timeout: 5 * time.Second},
			Token:   "t",
		}

		err := gh.BlockUser(context.Background(), "spammer")
		if err == nil {
			t.Fatal("expected error for invalid base URL")
		}
	})
}

func TestProcessFollowers(t *testing.T) {
	t.Parallel()

	t.Run("blocks expected users", func(t *testing.T) {
		t.Parallel()
		st := &srvState{}
		srv := newTestServer(t, st)
		t.Cleanup(srv.Close)

		st.mu.Lock()
		st.following["spam1"] = 50000 // should be blocked
		st.following["spam2"] = 25000 // should be blocked
		st.following["ok1"] = 100     // should NOT be blocked
		st.following["ok2"] = 19999   // should NOT be blocked
		st.following["flaky"] = 25000 // API error should cause skip
		st.errorUsers["flaky"] = true
		st.mu.Unlock()

		cfg := Config{
			Username:      "alice",
			PAT:           "t",
			Threshold:     20000,
			Whitelist:     map[string]struct{}{"ok2": {}},
			MaxConcurrent: 3,
		}
		gh := newTestClient(srv.URL)

		followers := []User{
			{Login: "spam1"},
			{Login: "spam2"},
			{Login: "ok1"},
			{Login: "ok2"},
			{Login: "flaky"},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		blocked := ProcessFollowers(ctx, gh, cfg, followers)
		if blocked != 2 {
			t.Errorf("blocked = %d, want 2", blocked)
		}

		st.mu.Lock()
		wantBlocked := map[string]bool{"spam1": true, "spam2": true}
		if !reflect.DeepEqual(st.blocked, wantBlocked) {
			t.Errorf("blocked users = %#v, want %#v", st.blocked, wantBlocked)
		}
		st.mu.Unlock()
	})

	t.Run("empty list", func(t *testing.T) {
		t.Parallel()
		gh := newTestClient("https://api.example.com")
		cfg := Config{
			Username:      "alice",
			PAT:           "t",
			Threshold:     20000,
			Whitelist:     map[string]struct{}{},
			MaxConcurrent: 3,
		}

		blocked := ProcessFollowers(context.Background(), gh, cfg, []User{})
		if blocked != 0 {
			t.Errorf("blocked = %d, want 0", blocked)
		}
	})

	t.Run("all whitelisted", func(t *testing.T) {
		t.Parallel()
		st := &srvState{}
		srv := newTestServer(t, st)
		t.Cleanup(srv.Close)

		st.mu.Lock()
		st.following["user1"] = 50000
		st.following["user2"] = 50000
		st.mu.Unlock()

		cfg := Config{
			Username:      "alice",
			PAT:           "t",
			Threshold:     20000,
			Whitelist:     map[string]struct{}{"user1": {}, "user2": {}},
			MaxConcurrent: 3,
		}
		gh := newTestClient(srv.URL)

		followers := []User{{Login: "user1"}, {Login: "user2"}}
		blocked := ProcessFollowers(context.Background(), gh, cfg, followers)
		if blocked != 0 {
			t.Errorf("blocked = %d, want 0 (all whitelisted)", blocked)
		}
	})

	t.Run("block failure", func(t *testing.T) {
		t.Parallel()
		var callCount atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/users/") && !strings.Contains(r.URL.Path, "followers") {
				json.NewEncoder(w).Encode(map[string]any{
					"login":     strings.TrimPrefix(r.URL.Path, "/users/"),
					"following": 50000,
				})
				return
			}
			if strings.HasPrefix(r.URL.Path, "/user/blocks/") {
				callCount.Add(1)
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"message": "forbidden"}`))
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)

		cfg := Config{
			Username:      "alice",
			PAT:           "t",
			Threshold:     20000,
			Whitelist:     map[string]struct{}{},
			MaxConcurrent: 1,
		}
		gh := newTestClient(srv.URL)

		followers := []User{{Login: "spammer1"}, {Login: "spammer2"}}
		blocked := ProcessFollowers(context.Background(), gh, cfg, followers)

		if blocked != 0 {
			t.Errorf("blocked = %d, want 0 (all blocks failed)", blocked)
		}
		if callCount.Load() != 2 {
			t.Errorf("block attempts = %d, want 2", callCount.Load())
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		t.Parallel()
		var requestCount atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount.Add(1)
			time.Sleep(20 * time.Millisecond)
			json.NewEncoder(w).Encode(map[string]any{
				"login":     "user",
				"following": 50000,
			})
		}))
		t.Cleanup(srv.Close)

		cfg := Config{
			Username:      "alice",
			PAT:           "t",
			Threshold:     20000,
			Whitelist:     map[string]struct{}{},
			MaxConcurrent: 1,
		}
		gh := newTestClient(srv.URL)

		followers := make([]User, 10)
		for i := range followers {
			followers[i] = User{Login: "user" + strconv.Itoa(i)}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()

		_ = ProcessFollowers(ctx, gh, cfg, followers)

		if requestCount.Load() >= 10 {
			t.Errorf("requestCount = %d, expected less than 10 due to cancellation", requestCount.Load())
		}
	})
}

// Test helpers

func newTestClient(baseURL string) *GitHubClient {
	return &GitHubClient{
		BaseURL:   baseURL,
		HTTP:      &http.Client{Timeout: 5 * time.Second},
		Token:     "test-token",
		UserAgent: "test-agent",
	}
}

type srvState struct {
	mu             sync.Mutex
	followersPages map[string][]User
	following      map[string]int
	blocked        map[string]bool
	errorUsers     map[string]bool
}

func newTestServer(t *testing.T, st *srvState) *httptest.Server {
	t.Helper()

	if st.followersPages == nil {
		st.followersPages = make(map[string][]User)
	}
	if st.following == nil {
		st.following = make(map[string]int)
	}
	if st.blocked == nil {
		st.blocked = make(map[string]bool)
	}
	if st.errorUsers == nil {
		st.errorUsers = make(map[string]bool)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/users/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) < 2 {
			http.NotFound(w, r)
			return
		}
		user := parts[1]

		// GET /users/{user}
		if len(parts) == 2 && r.Method == http.MethodGet {
			st.mu.Lock()
			isError := st.errorUsers[user]
			following := st.following[user]
			st.mu.Unlock()

			if isError {
				http.Error(w, "boom", http.StatusBadGateway)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"login":     user,
				"following": following,
			})
			return
		}

		// GET /users/{user}/followers
		if len(parts) == 3 && parts[2] == "followers" && r.Method == http.MethodGet {
			q := r.URL.Query()
			if q.Get("page") == "" {
				q.Set("page", "1")
			}

			key := r.URL.Path + "?" + q.Encode()

			st.mu.Lock()
			pageData := st.followersPages[key]
			st.mu.Unlock()

			if pageData == nil {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("[]"))
				return
			}

			nextQ := cloneValues(q)
			currPage, _ := strconv.Atoi(q.Get("page"))
			nextQ.Set("page", strconv.Itoa(currPage+1))
			nextKey := r.URL.Path + "?" + nextQ.Encode()

			st.mu.Lock()
			_, hasNext := st.followersPages[nextKey]
			st.mu.Unlock()

			if hasNext {
				u := url.URL{Scheme: "http", Host: r.Host, Path: r.URL.Path, RawQuery: nextQ.Encode()}
				w.Header().Set("Link", `<`+u.String()+`>; rel="next"`)
			}

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(pageData)
			return
		}

		http.NotFound(w, r)
	})

	// PUT /user/blocks/{username}
	mux.HandleFunc("/user/blocks/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		username := strings.TrimPrefix(r.URL.Path, "/user/blocks/")
		username = strings.Trim(username, "/")

		st.mu.Lock()
		st.blocked[username] = true
		st.mu.Unlock()

		w.WriteHeader(http.StatusNoContent)
	})

	return httptest.NewServer(mux)
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		cp := make([]string, len(vs))
		copy(cp, vs)
		out[k] = cp
	}
	return out
}
