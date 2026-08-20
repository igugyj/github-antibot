package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- config ---

func TestLoadConfig(t *testing.T) {
	t.Parallel()

	t.Run("defaults and overrides", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		wl := filepath.Join(dir, "wl.txt")
		bl := filepath.Join(dir, "bl.txt")
		os.WriteFile(wl, []byte("Bob\n# comment\ncarol\n\n"), 0o644)
		os.WriteFile(bl, []byte("Spammer1\n"), 0o644)

		cfgJSON := `{
			"username": "alice",
			"threshold": 0,
			"whitelist_file": "` + filepath.ToSlash(wl) + `",
			"blacklist_file": "` + filepath.ToSlash(bl) + `",
			"concurrency": 0,
			"timeout_sec": 0,
			"data_dir": ""
		}`
		cfgPath := filepath.Join(dir, "cfg.json")
		os.WriteFile(cfgPath, []byte(cfgJSON), 0o644)

		cfg, err := LoadConfig(cfgPath, "topsecret")
		if err != nil {
			t.Fatalf("LoadConfig error: %v", err)
		}
		if cfg.Username != "alice" {
			t.Errorf("Username = %q, want alice", cfg.Username)
		}
		if cfg.PAT != "topsecret" {
			t.Errorf("PAT = %q, want topsecret", cfg.PAT)
		}
		if cfg.Threshold != defaultThreshold {
			t.Errorf("Threshold = %d, want %d", cfg.Threshold, defaultThreshold)
		}
		if cfg.Concurrency != defaultConcurrency {
			t.Errorf("Concurrency = %d, want %d", cfg.Concurrency, defaultConcurrency)
		}
		if cfg.TimeoutSec != defaultTimeoutSec {
			t.Errorf("TimeoutSec = %d, want %d", cfg.TimeoutSec, defaultTimeoutSec)
		}
		if cfg.DataDir != defaultDataDir {
			t.Errorf("DataDir = %q, want %q", cfg.DataDir, defaultDataDir)
		}
		// case-insensitive, comments stripped
		for _, u := range []string{"bob", "BOB", "Carol"} {
			if !cfg.IsWhitelisted(u) {
				t.Errorf("IsWhitelisted(%q) = false, want true", u)
			}
		}
		if cfg.IsWhitelisted("dave") {
			t.Error("IsWhitelisted(dave) = true, want false")
		}
		if !cfg.IsBlacklisted("spammer1") || !cfg.IsBlacklisted("SPAMMER1") {
			t.Error("blacklist did not match case-insensitively")
		}
	})

	t.Run("explicit values", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "cfg.json")
		os.WriteFile(cfgPath, []byte(`{"username":"u","threshold":123,"concurrency":5,"timeout_sec":9}`), 0o644)
		cfg, err := LoadConfig(cfgPath, "t")
		if err != nil {
			t.Fatalf("LoadConfig error: %v", err)
		}
		if cfg.Threshold != 123 || cfg.Concurrency != 5 || cfg.TimeoutSec != 9 {
			t.Errorf("got threshold=%d concurrency=%d timeout=%d, want 123/5/9",
				cfg.Threshold, cfg.Concurrency, cfg.TimeoutSec)
		}
	})

	t.Run("missing username", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "cfg.json")
		os.WriteFile(cfgPath, []byte(`{"username":""}`), 0o644)
		if _, err := LoadConfig(cfgPath, "t"); err == nil {
			t.Error("expected error for missing username")
		}
	})

	t.Run("missing token", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "cfg.json")
		os.WriteFile(cfgPath, []byte(`{"username":"u"}`), 0o644)
		if _, err := LoadConfig(cfgPath, ""); err == nil {
			t.Error("expected error for missing token")
		}
	})

	t.Run("missing config file", func(t *testing.T) {
		t.Parallel()
		if _, err := LoadConfig(filepath.Join(t.TempDir(), "nope.json"), "t"); err == nil {
			t.Error("expected error for missing config file")
		}
	})

	t.Run("missing list files mean empty lists", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "cfg.json")
		os.WriteFile(cfgPath, []byte(`{"username":"u","whitelist_file":"`+filepath.ToSlash(filepath.Join(dir, "none.txt"))+`"}`), 0o644)
		cfg, err := LoadConfig(cfgPath, "t")
		if err != nil {
			t.Fatalf("LoadConfig error: %v", err)
		}
		if cfg.IsWhitelisted("anything") {
			t.Error("missing whitelist file should produce an empty list")
		}
	})
}

// --- storage ---

func TestBlockStore(t *testing.T) {
	t.Parallel()

	t.Run("add contains save load roundtrip", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "blocked.json")

		s := NewBlockStore()
		if !s.Add("SpamBot", 45000, "threshold") {
			t.Error("first Add should report new")
		}
		if s.Add("spambot", 45000, "threshold") { // case-insensitive dedup
			t.Error("second Add with different case should not report new")
		}
		if !s.Contains("SPAMBOT") {
			t.Error("Contains should be case-insensitive")
		}
		if !s.Add("other", 10, "blacklist") {
			t.Error("Add of distinct user should report new")
		}
		if err := s.Save(path); err != nil {
			t.Fatalf("Save error: %v", err)
		}

		// unchanged store does not rewrite the file
		if err := s.Save(path); err != nil {
			t.Fatalf("second Save error: %v", err)
		}

		loaded, err := LoadBlockStore(path)
		if err != nil {
			t.Fatalf("LoadBlockStore error: %v", err)
		}
		if loaded.Len() != 2 {
			t.Errorf("Len = %d, want 2", loaded.Len())
		}
		if !loaded.Contains("spambot") || !loaded.Contains("other") {
			t.Error("loaded store missing entries")
		}
	})

	t.Run("missing file is empty", func(t *testing.T) {
		t.Parallel()
		s, err := LoadBlockStore(filepath.Join(t.TempDir(), "nope.json"))
		if err != nil {
			t.Fatalf("LoadBlockStore error: %v", err)
		}
		if s.Len() != 0 {
			t.Error("missing file should produce empty store")
		}
	})

	t.Run("corrupt file errors", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "blocked.json")
		os.WriteFile(path, []byte("not json"), 0o644)
		if _, err := LoadBlockStore(path); err == nil {
			t.Error("expected error for corrupt store file")
		}
	})
}

// --- GitHub client ---

func TestParseNextLink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		link   string
		wantEP string
		wantOK bool
	}{
		{"empty", "", "", false},
		{"single next", `<https://api.github.com/foo?page=2>; rel="next"`, "/foo?page=2", true},
		{"multiple links picks next", `<https://api.github.com/foo?page=2>; rel="next", <https://api.github.com/foo?page=34>; rel="last"`, "/foo?page=2", true},
		{"no next", `<https://api.github.com/foo?page=34>; rel="last"`, "", false},
		{"malformed url", `<::::>; rel="next"`, "", false},
		{"absolute URL extracts path", `<https://example.com/users/u/followers?per_page=100&page=3>; rel="next"`, "/users/u/followers?per_page=100&page=3", true},
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
	if gh.BaseURL != githubBaseURL || gh.Token != "mytoken" || gh.UserAgent != "myagent" {
		t.Errorf("unexpected client fields: %+v", gh)
	}
	if gh.HTTP == nil || gh.HTTP.Timeout != 10*time.Second {
		t.Error("HTTP client missing or wrong timeout")
	}
}

func TestNewRequest(t *testing.T) {
	t.Parallel()

	t.Run("sets headers correctly", func(t *testing.T) {
		t.Parallel()
		gh := &GitHubClient{BaseURL: "https://api.example.com", Token: "testtoken", UserAgent: "testagent"}
		req, err := gh.newRequest(context.Background(), http.MethodGet, "/test", nil)
		if err != nil {
			t.Fatalf("newRequest error: %v", err)
		}
		want := map[string]string{
			"Authorization":        "Bearer testtoken",
			"Accept":               "application/vnd.github+json",
			"X-GitHub-Api-Version": apiVersion,
			"User-Agent":           "testagent",
		}
		for h, w := range want {
			if got := req.Header.Get(h); got != w {
				t.Errorf("%s = %q, want %q", h, got, w)
			}
		}
	})

	t.Run("invalid URL returns error", func(t *testing.T) {
		t.Parallel()
		gh := &GitHubClient{BaseURL: "://invalid-url"}
		_, err := gh.newRequest(context.Background(), http.MethodGet, "/test", nil)
		if err == nil || !strings.Contains(err.Error(), "failed to create request") {
			t.Errorf("error = %v, want 'failed to create request'", err)
		}
	})
}

type failingRoundTripper struct{ err error }

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
		gh := &GitHubClient{BaseURL: "https://api.example.com", HTTP: newFailingClient(errors.New("connection refused")), Token: "t"}
		req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/test", nil)
		_, err := gh.do(req)
		if err == nil || !strings.Contains(err.Error(), "failed to do request") {
			t.Errorf("error = %v, want 'failed to do request'", err)
		}
	})

	t.Run("error response truncation", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(strings.Repeat("x", 10000)))
		}))
		t.Cleanup(srv.Close)

		gh := &GitHubClient{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 5 * time.Second}, Token: "t"}
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/test", nil)
		_, err := gh.do(req)
		if err == nil {
			t.Fatal("expected error for 500 response")
		}
		if len(err.Error()) > 9000 {
			t.Errorf("error message too long (%d chars)", len(err.Error()))
		}
	})

	t.Run("transient 429 is retried then succeeds", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) <= 2 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(srv.Close)

		gh := &GitHubClient{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 5 * time.Second}, Token: "t"}
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/test", nil)
		resp, err := gh.do(req)
		if err != nil {
			t.Fatalf("do error after transient failures: %v", err)
		}
		resp.Body.Close()
		if calls.Load() != 3 {
			t.Errorf("calls = %d, want 3", calls.Load())
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

		q1 := url.Values{"per_page": {"100"}, "page": {"1"}}
		q2 := url.Values{"per_page": {"100"}, "page": {"2"}}
		st.mu.Lock()
		st.followersPages["/users/alice/followers?"+q1.Encode()] = []User{{Login: "u1"}, {Login: "u2"}}
		st.followersPages["/users/alice/followers?"+q2.Encode()] = []User{{Login: "u3"}}
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
			w.Write([]byte("[]"))
		}))
		t.Cleanup(srv.Close)
		gh := newTestClient(srv.URL)
		followers, err := gh.GetFollowers(context.Background(), "alice")
		if err != nil || len(followers) != 0 {
			t.Errorf("got %v followers, err %v; want 0, nil", len(followers), err)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("not valid json"))
		}))
		t.Cleanup(srv.Close)
		gh := newTestClient(srv.URL)
		_, err := gh.GetFollowers(context.Background(), "alice")
		if err == nil || !strings.Contains(err.Error(), "failed to decode followers") {
			t.Errorf("error = %v, want 'failed to decode followers'", err)
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
		if err == nil || !strings.Contains(err.Error(), "failed to get followers page") {
			t.Errorf("error = %v, want 'failed to get followers page'", err)
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
		count, err := newTestClient(srv.URL).GetFollowingCount(context.Background(), "alice")
		if err != nil || count != 42 {
			t.Errorf("count = %d, err %v; want 42, nil", count, err)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("not valid json"))
		}))
		t.Cleanup(srv.Close)
		_, err := newTestClient(srv.URL).GetFollowingCount(context.Background(), "alice")
		if err == nil || !strings.Contains(err.Error(), "failed to decode user") {
			t.Errorf("error = %v, want 'failed to decode user'", err)
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

		err := newTestClient(srv.URL).BlockUser(context.Background(), "spammer")
		if err != nil || !blocked.Load() {
			t.Errorf("err = %v, blocked = %v; want nil, true", err, blocked.Load())
		}
	})

	t.Run("API error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"message": "forbidden"}`))
		}))
		t.Cleanup(srv.Close)
		err := newTestClient(srv.URL).BlockUser(context.Background(), "spammer")
		if err == nil || !strings.Contains(err.Error(), "failed to block") {
			t.Errorf("error = %v, want 'failed to block'", err)
		}
	})
}

func TestOpenIssue(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		var gotTitle, gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/repos/alice/github-antibot/issues" {
				http.NotFound(w, r)
				return
			}
			var p issuePayload
			json.NewDecoder(r.Body).Decode(&p)
			gotTitle, gotBody = p.Title, p.Body
			w.WriteHeader(http.StatusCreated)
		}))
		t.Cleanup(srv.Close)

		err := newTestClient(srv.URL).OpenIssue(context.Background(), "alice/github-antibot", "t", "b")
		if err != nil || gotTitle != "t" || gotBody != "b" {
			t.Errorf("err = %v, title = %q, body = %q; want nil, t, b", err, gotTitle, gotBody)
		}
	})

	t.Run("invalid repo", func(t *testing.T) {
		t.Parallel()
		err := newTestClient("https://api.example.com").OpenIssue(context.Background(), "../evil", "t", "b")
		if err == nil {
			t.Error("expected error for invalid repo")
		}
	})
}

// --- processor ---

func TestProcessFollowers(t *testing.T) {
	t.Parallel()

	cfg := func() Config {
		return Config{
			Username:    "alice",
			Threshold:   20000,
			Concurrency: 3,
			whitelistL:  map[string]struct{}{},
			blacklistL:  map[string]struct{}{},
		}
	}

	t.Run("threshold, whitelist, blacklist, dedup", func(t *testing.T) {
		t.Parallel()
		st := &srvState{}
		srv := newTestServer(t, st)
		t.Cleanup(srv.Close)

		st.mu.Lock()
		st.following["spam1"] = 50000     // threshold -> block
		st.following["ok1"] = 100         // ok
		st.following["black1"] = 5        // blacklist -> block (no lookup needed)
		st.following["already1"] = 999999 // already blocked -> skip (no lookup)
		st.errorUsers["flaky"] = true     // API error -> skipped
		st.following["flaky"] = 25000
		st.mu.Unlock()

		c := cfg()
		c.whitelistL["ok1"] = struct{}{} // whitelist wins over threshold? no, under threshold too
		c.blacklistL["black1"] = struct{}{}
		store := NewBlockStore()
		store.Add("already1", 999999, "threshold")

		gh := newTestClient(srv.URL)
		results := ProcessFollowers(context.Background(), gh, c, store, []User{
			{Login: "spam1"}, {Login: "ok1"}, {Login: "black1"},
			{Login: "already1"}, {Login: "flaky"},
		})

		byUser := map[string]string{}
		for _, r := range results {
			byUser[strings.ToLower(r.Username)] = r.Action
		}
		want := map[string]string{
			"spam1":    "blocked",
			"ok1":      "whitelisted",
			"black1":   "blocked",
			"already1": "already_blocked",
			"flaky":    "skipped",
		}
		if !reflect.DeepEqual(byUser, want) {
			t.Errorf("actions = %#v, want %#v", byUser, want)
		}

		// blacklist user blocked without a following lookup
		st.mu.Lock()
		if st.followingLookups["black1"] {
			t.Error("blacklisted user should not trigger a following lookup")
		}
		if st.followingLookups["already1"] {
			t.Error("already-blocked user should not trigger a following lookup")
		}
		blocked := map[string]bool{}
		for u := range st.blocked {
			blocked[u] = true
		}
		st.mu.Unlock()
		if !blocked["spam1"] || !blocked["black1"] {
			t.Errorf("blocked = %#v, want spam1 and black1", blocked)
		}
		if blocked["ok1"] || blocked["already1"] {
			t.Errorf("blocked = %#v, overshoot", blocked)
		}
	})

	t.Run("dry run blocks nothing", func(t *testing.T) {
		t.Parallel()
		st := &srvState{}
		srv := newTestServer(t, st)
		t.Cleanup(srv.Close)
		st.mu.Lock()
		st.following["spam1"] = 50000
		st.mu.Unlock()

		c := cfg()
		c.DryRun = true
		results := ProcessFollowers(context.Background(), newTestClient(srv.URL), c, NewBlockStore(), []User{{Login: "spam1"}})

		if len(results) != 1 || results[0].Action != "would_block" {
			t.Errorf("results = %#v, want single would_block", results)
		}
		if len(st.blocked) != 0 {
			t.Errorf("blocked = %#v, want none in dry run", st.blocked)
		}
	})

	t.Run("block failure recorded", func(t *testing.T) {
		t.Parallel()
		var callCount atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/users/") && !strings.Contains(r.URL.Path, "followers") {
				json.NewEncoder(w).Encode(map[string]any{"login": strings.TrimPrefix(r.URL.Path, "/users/"), "following": 50000})
				return
			}
			if strings.HasPrefix(r.URL.Path, "/user/blocks/") {
				callCount.Add(1)
				w.WriteHeader(http.StatusForbidden)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)

		c := cfg()
		results := ProcessFollowers(context.Background(), newTestClient(srv.URL), c, NewBlockStore(),
			[]User{{Login: "spammer1"}, {Login: "spammer2"}})
		if countAction(results, "block_failed") != 2 {
			t.Errorf("block_failed = %d, want 2", countAction(results, "block_failed"))
		}
		if callCount.Load() != 2 {
			t.Errorf("block attempts = %d, want 2", callCount.Load())
		}
	})

	t.Run("empty list", func(t *testing.T) {
		t.Parallel()
		results := ProcessFollowers(context.Background(), newTestClient("https://api.example.com"), cfg(), NewBlockStore(), []User{})
		if len(results) != 0 {
			t.Errorf("results = %#v, want empty", results)
		}
	})
}

// --- report ---

func TestBuildReport(t *testing.T) {
	t.Parallel()

	results := []ActionResult{
		{Username: "spam2", Following: 45000, Action: "blocked", Reason: "following 45000 >= threshold 20000"},
		{Username: "spam1", Following: 30000, Action: "blocked", Reason: "following 30000 >= threshold 20000"},
		{Username: "wp1", Following: 30000, Action: "would_block", Reason: "following 30000 >= threshold 20000"},
		{Username: "old", Following: 50000, Action: "already_blocked", Reason: "persisted block"},
		{Username: "trusted", Following: 90000, Action: "whitelisted", Reason: "whitelist"},
	}
	cfg := Config{Username: "alice", Threshold: 20000}
	report := BuildReport("2025-01-15", cfg, 5, results)

	for _, want := range []string{
		"# Antibot Report — 2025-01-15",
		"Target: `alice`",
		"Threshold: 20000",
		"Followers scanned: 5",
		"Newly blocked: 2",
		"Would block (dry run): 1",
		"Already blocked: 1",
		"Whitelisted: 1",
		"| spam1 | 30000 |",
		"| spam2 | 45000 |",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q\n---\n%s", want, report)
		}
	}
	// sorted: spam1 before spam2
	if strings.Index(report, "spam1") > strings.Index(report, "spam2") {
		t.Error("newly blocked rows not sorted")
	}
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
	mu               sync.Mutex
	followersPages   map[string][]User
	following        map[string]int
	blocked          map[string]bool
	errorUsers       map[string]bool
	followingLookups map[string]bool
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
	if st.followingLookups == nil {
		st.followingLookups = make(map[string]bool)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/users/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 2 {
			http.NotFound(w, r)
			return
		}
		user := parts[1]

		if len(parts) == 2 && r.Method == http.MethodGet {
			st.mu.Lock()
			isError := st.errorUsers[user]
			following := st.following[user]
			st.followingLookups[user] = true
			st.mu.Unlock()

			if isError {
				http.Error(w, "boom", http.StatusBadGateway)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"login": user, "following": following})
			return
		}

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
				w.Write([]byte("[]"))
				return
			}
			nextQ := cloneValues(q)
			currPage, _ := strconv.Atoi(q.Get("page"))
			nextQ.Set("page", strconv.Itoa(currPage+1))
			st.mu.Lock()
			_, hasNext := st.followersPages[r.URL.Path+"?"+nextQ.Encode()]
			st.mu.Unlock()
			if hasNext {
				u := url.URL{Scheme: "http", Host: r.Host, Path: r.URL.Path, RawQuery: nextQ.Encode()}
				w.Header().Set("Link", `<`+u.String()+`>; rel="next"`)
			}
			json.NewEncoder(w).Encode(pageData)
			return
		}
		http.NotFound(w, r)
	})

	mux.HandleFunc("/user/blocks/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		username := strings.Trim(strings.TrimPrefix(r.URL.Path, "/user/blocks/"), "/")
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
