package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// BuildReport renders the daily markdown report. It is written to
// data/reports/<date>.md, echoed to the workflow step summary, and used as the
// body of the GitHub issue when new users were blocked.
func BuildReport(date string, cfg Config, scanned int, results []ActionResult) string {
	var newly, would, already, whitelisted []ActionResult
	for _, r := range results {
		switch r.Action {
		case "blocked":
			newly = append(newly, r)
		case "would_block":
			would = append(would, r)
		case "already_blocked":
			already = append(already, r)
		case "whitelisted":
			whitelisted = append(whitelisted, r)
		}
	}
	sortResults(newly)
	sortResults(would)
	sortResults(already)
	sortResults(whitelisted)

	var b strings.Builder
	fmt.Fprintf(&b, "# Antibot Report — %s\n\n", date)
	fmt.Fprintf(&b, "- Target: `%s`\n", cfg.Username)
	fmt.Fprintf(&b, "- Threshold: %d\n", cfg.Threshold)
	if cfg.DryRun {
		b.WriteString("- Mode: **dry run** (no user was blocked)\n")
	}
	fmt.Fprintf(&b, "- Followers scanned: %d\n", scanned)
	fmt.Fprintf(&b, "- Newly blocked: %d\n", len(newly))
	fmt.Fprintf(&b, "- Would block (dry run): %d\n", len(would))
	fmt.Fprintf(&b, "- Already blocked: %d\n", len(already))
	fmt.Fprintf(&b, "- Whitelisted: %d\n\n", len(whitelisted))

	if len(newly) > 0 {
		b.WriteString("## Newly blocked\n\n| username | following | reason |\n|---|---|---|\n")
		for _, r := range newly {
			fmt.Fprintf(&b, "| %s | %d | %s |\n", r.Username, r.Following, r.Reason)
		}
		b.WriteString("\n")
	}
	if len(would) > 0 {
		b.WriteString("## Would block (dry run)\n\n| username | following | reason |\n|---|---|---|\n")
		for _, r := range would {
			fmt.Fprintf(&b, "| %s | %d | %s |\n", r.Username, r.Following, r.Reason)
		}
		b.WriteString("\n")
	}
	if len(already) > 0 {
		b.WriteString("## Already blocked\n\n| username | reason |\n|---|---|\n")
		for _, r := range already {
			fmt.Fprintf(&b, "| %s | %s |\n", r.Username, r.Reason)
		}
		b.WriteString("\n")
	}
	if len(whitelisted) > 0 {
		b.WriteString("## Whitelisted (skipped)\n\n")
		for _, r := range whitelisted {
			fmt.Fprintf(&b, "- %s\n", r.Username)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func sortResults(rs []ActionResult) {
	sort.Slice(rs, func(i, j int) bool {
		return strings.ToLower(rs[i].Username) < strings.ToLower(rs[j].Username)
	})
}

type issuePayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// OpenIssue creates an issue in the given repo ("owner/name"). Used to notify
// the user when new users were blocked.
func (c *GitHubClient) OpenIssue(ctx context.Context, repo, title, body string) error {
	if strings.HasPrefix(repo, "/") || strings.Contains(repo, "..") {
		return fmt.Errorf("invalid issue repo %q", repo)
	}
	payload, err := json.Marshal(issuePayload{Title: title, Body: body})
	if err != nil {
		return err
	}
	req, err := c.newRequest(
		ctx,
		http.MethodPost,
		"/repos/"+url.PathEscape(repo)+"/issues",
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("failed to open issue: %w", err)
	}
	resp.Body.Close()
	return nil
}
