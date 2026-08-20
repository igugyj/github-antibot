package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	configPath := flag.String("config", "config.json", "path to the config file")
	flag.Parse()

	cfg, err := LoadConfig(*configPath, os.Getenv("GH_PAT"))
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSec)*time.Second)
	defer cancel()

	gh := NewGitHubClient(cfg.PAT, cfg.Username)

	blockedPath := filepath.Join(cfg.DataDir, "blocked.json")
	store, err := LoadBlockStore(blockedPath)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("loaded %d previously blocked users", store.Len())

	log.Printf("fetching followers for %s...", cfg.Username)
	followers, err := gh.GetFollowers(ctx, cfg.Username)
	if err != nil {
		log.Fatalf("failed to fetch followers: %v", err)
	}
	log.Printf("found %d followers", len(followers))

	results := ProcessFollowers(ctx, gh, cfg, store, followers)

	// Persist only real blocks; dry-run candidates stay out of the store.
	for _, r := range results {
		if r.Action == "blocked" {
			store.Add(r.Username, r.Following, r.Reason)
		}
	}
	if err := store.Save(blockedPath); err != nil {
		log.Printf("failed to save blocked store: %v", err)
	}

	date := time.Now().UTC().Format("2006-01-02")
	report := BuildReport(date, cfg, len(followers), results)

	reportPath := filepath.Join(cfg.DataDir, "reports", date+".md")
	if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
		log.Printf("failed to mkdir reports dir: %v", err)
	} else if err := os.WriteFile(reportPath, []byte(report), 0o644); err != nil {
		log.Printf("failed to write report: %v", err)
	}
	// The report on stdout is captured by scripts/run.sh and appended to the
	// GitHub Actions step summary.
	fmt.Print(report)

	if err := cleanupReports(filepath.Join(cfg.DataDir, "reports"), cfg.Report.MaxReports); err != nil {
		log.Printf("failed to clean up old reports: %v", err)
	}

	newlyBlocked := countAction(results, "blocked")
	if newlyBlocked > 0 && cfg.Report.Issue && cfg.Report.IssueRepo != "" {
		title := fmt.Sprintf("Antibot: blocked %d user(s) on %s", newlyBlocked, date)
		if err := gh.OpenIssue(ctx, cfg.Report.IssueRepo, title, report); err != nil {
			log.Printf("failed to open issue: %v", err)
		}
	}
	log.Printf("finished. blocked %d users.", newlyBlocked)
}
