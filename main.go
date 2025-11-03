package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	defaultThreshold   = 20000
	defaultConcurrency = 10
	githubBaseURL      = "https://api.github.com"
	apiVersion         = "2022-11-28"
)

type Config struct {
	Username      string
	PAT           string
	Threshold     int
	Whitelist     map[string]struct{}
	MaxConcurrent int
}

func ParseConfig(ghUsername, ghPat, thresholdStr, whitelistStr, concurrencyStr string) (Config, error) {
	if ghUsername == "" {
		return Config{}, errors.New("GH_USERNAME is required")
	}
	if ghPat == "" {
		return Config{}, errors.New("GH_PAT is required")
	}

	threshold := defaultThreshold
	if s := strings.TrimSpace(thresholdStr); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			threshold = v
		}
	}

	maxConc := defaultConcurrency
	if s := strings.TrimSpace(concurrencyStr); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			maxConc = v
		}
	}

	wl := parseWhitelist(whitelistStr)

	return Config{
		Username:      ghUsername,
		PAT:           ghPat,
		Threshold:     threshold,
		Whitelist:     wl,
		MaxConcurrent: maxConc,
	}, nil
}

func parseWhitelist(s string) map[string]struct{} {
	if strings.TrimSpace(s) == "" {
		return map[string]struct{}{}
	}
	items := strings.Split(s, ",")
	out := make(map[string]struct{}, len(items))
	for _, it := range items {
		if v := strings.TrimSpace(it); v != "" {
			out[v] = struct{}{}
		}
	}
	return out
}

type GitHubClient struct {
	BaseURL   string
	HTTP      *http.Client
	Token     string
	UserAgent string
}

func NewGitHubClient(token, userAgent string) *GitHubClient {
	return &GitHubClient{
		BaseURL: githubBaseURL,
		HTTP: &http.Client{
			Timeout: 10 * time.Second,
		},
		Token:     token,
		UserAgent: userAgent,
	}
}

func (c *GitHubClient) newRequest(
	ctx context.Context,
	method, endpoint string,
	body io.Reader,
) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", c.UserAgent)
	return req, nil
}

func (c *GitHubClient) do(req *http.Request) (*http.Response, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to do request: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		const maxErr = 8 << 10 // 8KB
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErr))
		return nil, fmt.Errorf(
			"github request returned status %s: %s",
			resp.Status,
			strings.TrimSpace(string(b)),
		)
	}
	return resp, nil
}

type User struct {
	Login     string `json:"login"`
	Following int    `json:"following"`
}

func (c *GitHubClient) GetFollowers(ctx context.Context, username string) ([]User, error) {
	var out []User
	endpoint := fmt.Sprintf("/users/%s/followers?per_page=100", url.PathEscape(username))

	for endpoint != "" {
		req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to get followers page: %w", err)
		}
		func() {
			defer resp.Body.Close()
			var page []User
			if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
				resp.Body.Close()
				err = fmt.Errorf("failed to decode followers: %w", err)
			} else {
				out = append(out, page...)
			}
		}()

		next, ok := parseNextLink(resp.Header.Get("Link"))
		if !ok {
			break
		}
		endpoint = next
	}
	return out, nil
}

func (c *GitHubClient) GetFollowingCount(ctx context.Context, username string) (int, error) {
	req, err := c.newRequest(
		ctx,
		http.MethodGet,
		fmt.Sprintf("/users/%s", url.PathEscape(username)),
		nil,
	)
	if err != nil {
		return 0, err
	}
	resp, err := c.do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to get user %s: %w", username, err)
	}
	defer resp.Body.Close()
	var u User
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return 0, fmt.Errorf("failed to decode user %s: %w", username, err)
	}
	return u.Following, nil
}

func (c *GitHubClient) BlockUser(ctx context.Context, username string) error {
	req, err := c.newRequest(
		ctx,
		http.MethodPut,
		fmt.Sprintf("/user/blocks/%s", url.PathEscape(username)),
		nil,
	)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("failed to block %s: %w", username, err)
	}
	resp.Body.Close()
	return nil
}

// extracts the RequestURI for rel="next" from a Link header.
// returns (endpoint, true) if present, otherwise ("", false).
func parseNextLink(linkHeader string) (string, bool) {
	if linkHeader == "" {
		return "", false
	}
	links := strings.Split(linkHeader, ",")
	for _, link := range links {
		link = strings.TrimSpace(link)
		if !strings.Contains(link, `rel="next"`) {
			continue
		}
		if urlPart, _, ok := strings.Cut(link, ";"); ok {
			nextURLStr := strings.Trim(urlPart, "<> \t")
			u, err := url.Parse(nextURLStr)
			if err != nil {
				return "", false
			}
			return u.RequestURI(), true
		}
	}
	return "", false
}

func ProcessFollowers(
	ctx context.Context,
	gh *GitHubClient,
	cfg Config,
	followers []User,
) (int64, error) {
	var blocked int64

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cfg.MaxConcurrent)

	for _, f := range followers {
		username := f.Login // capture
		if _, ok := cfg.Whitelist[username]; ok {
			log.Printf("skip whitelisted: %s", username)
			continue
		}

		g.Go(func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			count, err := gh.GetFollowingCount(ctx, username)
			if err != nil {
				log.Printf("failed to get following count for %s: %v", username, err)
				return nil // do not block
			}

			if count >= cfg.Threshold {
				log.Printf(
					"blocking %s: following %d >= threshold %d",
					username,
					count,
					cfg.Threshold,
				)
				if err := gh.BlockUser(ctx, username); err != nil {
					log.Printf("block %s failed: %v", username, err)
					return nil
				}
				atomic.AddInt64(&blocked, 1)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return blocked, err
	}
	return blocked, nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	ghUsername := os.Getenv("GH_USERNAME")
	ghPat := os.Getenv("GH_PAT")
	thresholdStr := os.Getenv("ANTIBOT_THRESHOLD")
	whitelistStr := os.Getenv("ANTIBOT_WHITELIST")
	concurrencyStr := os.Getenv("ANTIBOT_CONCURRENCY") // optional

	cfg, err := ParseConfig(ghUsername, ghPat, thresholdStr, whitelistStr, concurrencyStr)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	gh := NewGitHubClient(cfg.PAT, cfg.Username)

	log.Printf("fetching followers for %s...", cfg.Username)
	followers, err := gh.GetFollowers(ctx, cfg.Username)
	if err != nil {
		log.Fatalf("failed to fetch followers: %v", err)
	}
	log.Printf("found %d followers", len(followers))

	blocked, err := ProcessFollowers(ctx, gh, cfg, followers)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("processing ended with error: %v", err)
	}
	log.Printf("finished. blocked %d users.", blocked)
}
