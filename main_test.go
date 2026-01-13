package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseConfig_DefaultsAndOverrides(t *testing.T) {
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
		t.Fatalf("Username = %q, want %q", cfg.Username, "alice")
	}
	if cfg.PAT != "topsecret" {
		t.Fatalf("PAT = %q, want %q", cfg.PAT, "topsecret")
	}
	if cfg.Threshold != defaultThreshold {
		t.Fatalf("Threshold = %d, want %d", cfg.Threshold, defaultThreshold)
	}
	if cfg.MaxConcurrent != 1 {
		t.Fatalf("MaxConcurrent = %d, want %d", cfg.MaxConcurrent, 1)
	}
	wantWL := map[string]struct{}{"bob": {}, "carol": {}}
	if !reflect.DeepEqual(cfg.Whitelist, wantWL) {
		t.Fatalf("Whitelist = %#v, want %#v", cfg.Whitelist, wantWL)
	}
}

func TestParseConfig_OverridesValid(t *testing.T) {
	cfg, err := ParseConfig("u", "t", "123", "x,y", "5")
	if err != nil {
		t.Fatalf("ParseConfig error: %v", err)
	}
	if cfg.Threshold != 123 {
		t.Fatalf("Threshold = %d, want %d", cfg.Threshold, 123)
	}
	if cfg.MaxConcurrent != 5 {
		t.Fatalf("MaxConcurrent = %d, want %d", cfg.MaxConcurrent, 5)
	}
}

func TestParseConfig_Errors(t *testing.T) {
	if _, err := ParseConfig("", "t", "", "", ""); err == nil {
		t.Fatal("expected error for missing username")
	}
	if _, err := ParseConfig("u", "", "", "", ""); err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestParseNextLink(t *testing.T) {
	tests := []struct {
		name   string
		link   string
		wantEP string
		wantOK bool
	}{
		{"empty", "", "", false},
		{
			"single next",
			`<https://api.github.com/foo?page=2>; rel="next"`,
			"/foo?page=2",
			true,
		},
		{
			"multiple links picks next",
			`<https://api.github.com/foo?page=2>; rel="next", <https://api.github.com/foo?page=34>; rel="last"`,
			"/foo?page=2",
			true,
		},
		{
			"no next",
			`<https://api.github.com/foo?page=34>; rel="last"`,
			"",
			false,
		},
		{
			"malformed url",
			`<::::>; rel="next"`,
			"",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep, ok := parseNextLink(tt.link)
			if ok != tt.wantOK || ep != tt.wantEP {
				t.Fatalf("parseNextLink() = (%q,%v), want (%q,%v)", ep, ok, tt.wantEP, tt.wantOK)
			}
		})
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
	st.mu = sync.Mutex{}
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

		// /users/{user}
		if len(parts) == 2 && r.Method == http.MethodGet {
			st.mu.Lock()
			defer st.mu.Unlock()
			if st.errorUsers[user] {
				http.Error(w, "boom", http.StatusBadGateway)
				return
			}
			type resp struct {
				Login     string `json:"login"`
				Following int    `json:"following"`
			}
			json.NewEncoder(w).Encode(resp{Login: user, Following: st.following[user]})
			return
		}

		// /users/{user}/followers
		if len(parts) == 3 && parts[2] == "followers" && r.Method == http.MethodGet {
			// normalize query: default page=1 when missing
			q := r.URL.Query()
			if q.Get("page") == "" {
				q.Set("page", "1")
			}

			// use normalized query to build the key we look up
			key := r.URL.Path + "?" + q.Encode()

			st.mu.Lock()
			pageData := st.followersPages[key]
			st.mu.Unlock()

			if pageData == nil {
				w.Header().Del("Link")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("[]"))
				return
			}

			// determine if a "next" page exists based on the normalized next key
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
			} else {
				w.Header().Del("Link")
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

func TestGetFollowers_Pagination(t *testing.T) {
	st := &srvState{}
	srv := newTestServer(t, st)
	t.Cleanup(srv.Close)

	// seed 2 pages: page=1 (2 users), page=2 (1 user)
	basePath := "/users/alice/followers"
	q1 := url.Values{"per_page": {"100"}, "page": {"1"}}
	q2 := url.Values{"per_page": {"100"}, "page": {"2"}}

	st.followersPages[basePath+"?"+q1.Encode()] = []User{
		{Login: "u1"}, {Login: "u2"},
	}
	st.followersPages[basePath+"?"+q2.Encode()] = []User{
		{Login: "u3"},
	}

	gh := &GitHubClient{
		BaseURL: srv.URL,
		HTTP:    &http.Client{Timeout: 5 * time.Second},
		Token:   "t",
		UserAgent: "alice",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := gh.GetFollowers(ctx, "alice")
	if err != nil {
		t.Fatalf("GetFollowers error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("followers len = %d, want 3; got = %#v", len(got), got)
	}
}

func TestProcessFollowers_BlocksExpectedUsers(t *testing.T) {
	st := &srvState{}
	srv := newTestServer(t, st)
	t.Cleanup(srv.Close)

	// following counts for users
	st.following["spam1"] = 50000 // should be blocked
	st.following["spam2"] = 25000 // should be blocked
	st.following["ok1"] = 100     // should NOT be blocked
	st.following["ok2"] = 19999   // should NOT be blocked
	st.following["flaky"] = 25000 // API error below should cause skip, not cancel

	// make one user error on detail fetch
	st.errorUsers["flaky"] = true

	cfg := Config{
		Username:      "alice",
		PAT:           "t",
		Threshold:     20000,
		Whitelist:     map[string]struct{}{"ok2": {}}, // whitelisted
		MaxConcurrent: 3,
	}
	gh := &GitHubClient{
		BaseURL:   srv.URL,
		HTTP:      &http.Client{Timeout: 5 * time.Second},
		Token:     "t",
		UserAgent: "alice",
	}

	followers := []User{
		{Login: "spam1"},
		{Login: "spam2"},
		{Login: "ok1"},
		{Login: "ok2"},
		{Login: "flaky"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	blocked, err := ProcessFollowers(ctx, gh, cfg, followers)
	if err != nil && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("ProcessFollowers unexpected error: %v", err)
	}

	if blocked != 2 {
		t.Fatalf("blocked = %d, want 2", blocked)
	}

	// verify which users got blocked.
	st.mu.Lock()
	defer st.mu.Unlock()
	wantBlocked := map[string]bool{"spam1": true, "spam2": true}
	if !reflect.DeepEqual(st.blocked, wantBlocked) {
		t.Fatalf("blocked users = %#v, want %#v", st.blocked, wantBlocked)
	}
}

func TestParseNextLink_AcceptsAbsoluteURL(t *testing.T) {
	link := `<https://example.com/users/u/followers?per_page=100&page=3>; rel="next"`
	ep, ok := parseNextLink(link)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ep != "/users/u/followers?per_page=100&page=3" {
		t.Fatalf("endpoint = %q, want %q", ep, "/users/u/followers?per_page=100&page=3")
	}
}
