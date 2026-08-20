package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	githubBaseURL = "https://api.github.com"
	apiVersion    = "2022-11-28"
	maxRetries    = 2 // additional attempts after the first
)

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
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", c.UserAgent)
	return req, nil
}

// do performs a request, retrying transient failures (429 and 5xx) with
// quadratic backoff. All requests in this program carry a nil body, so
// re-sending the cloned request is safe.
func (c *GitHubClient) do(req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt*attempt) * 500 * time.Millisecond
			select {
			case <-req.Context().Done():
				return nil, lastErr
			case <-time.After(delay):
			}
		}
		resp, err := c.HTTP.Do(req.Clone(req.Context()))
		if err != nil {
			lastErr = fmt.Errorf("failed to do request: %w", err)
			if attempt < maxRetries {
				continue
			}
			return nil, lastErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			status := resp.StatusCode
			const maxErr = 8 << 10 // 8KB
			b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErr))
			resp.Body.Close()
			lastErr = fmt.Errorf(
				"github request returned status %s: %s",
				resp.Status,
				strings.TrimSpace(string(b)),
			)
			if (status == http.StatusTooManyRequests || status >= 500) && attempt < maxRetries {
				continue
			}
			return nil, lastErr
		}
		return resp, nil
	}
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

		var page []User
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to decode followers: %w", err)
		}
		out = append(out, page...)

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

// parseNextLink extracts the RequestURI for rel="next" from a Link header.
// It returns (endpoint, true) if present, otherwise ("", false).
func parseNextLink(linkHeader string) (string, bool) {
	for link := range strings.SplitSeq(linkHeader, ",") {
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
