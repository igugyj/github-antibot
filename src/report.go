package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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

// cleanupReports deletes report files beyond the newest max. Called after each
// run, so the reports directory never grows past maxReports files.
func cleanupReports(dir string, max int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) <= max {
		return nil
	}
	removed := 0
	for _, n := range names[:len(names)-max] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return err
		}
		removed++
	}
	if removed > 0 {
		fmt.Printf("removed %d old report(s), keeping newest %d\n", removed, max)
	}
	return nil
}

type issuePayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// OpenIssue creates an issue in the given repo ("owner/name"). Used to notify
// the user when new users were blocked.
func (c *GitHubClient) OpenIssue(ctx context.Context, repo, title, body string) error {
	// Validate repo format: exactly "owner/name".
	if strings.HasPrefix(repo, "/") || strings.Contains(repo, "..") {
		return fmt.Errorf("invalid issue repo %q", repo)
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid repo format %q, expected 'owner/name'", repo)
	}

	payload, err := json.Marshal(issuePayload{Title: title, Body: body})
	if err != nil {
		return err
	}
	// Escape owner and name separately: escaping "owner/name" as a whole turns
	// the "/" into "%2F", which GitHub's routing rejects.
	req, err := c.newRequest(
		ctx,
		http.MethodPost,
		"/repos/"+url.PathEscape(parts[0])+"/"+url.PathEscape(parts[1])+"/issues",
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("failed to open issue: %w", err)
	}
	resp.Body.Close()
	return nil
}

// smokeTestIssue verifies the issue-reporting path end-to-end by creating a
// real issue. Enabled with ANTIBOT_SMOKE_TEST=1 so scheduled runs stay quiet.
func smokeTestIssue(gh *GitHubClient, cfg Config) {
	if os.Getenv("ANTIBOT_SMOKE_TEST") != "1" {
		return
	}
	if cfg.Report.IssueRepo == "" {
		log.Printf("smoke test: skipped, report.issue_repo is empty")
		return
	}
	// Independent context: the main scan timeout may already be spent.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	title := "Antibot smoke test " + time.Now().UTC().Format("20060102-150405")
	body := "Automated test of the issue-reporting path. Safe to close and delete.\n\nIf you are reading this, OpenIssue works."
	if err := gh.OpenIssue(ctx, cfg.Report.IssueRepo, title, body); err != nil {
		log.Printf("smoke test: FAILED to open issue: %v", err)
		return
	}
	log.Printf("smoke test: OK, issue created in %s: %q", cfg.Report.IssueRepo, title)
}
