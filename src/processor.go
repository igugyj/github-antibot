package main

import (
	"context"
	"fmt"
	"log"
	"sync"
)

// boundedPool runs fns concurrently with at most n in flight. Errors are not
// propagated: callers here only log them, so this is equivalent to the
// errgroup usage it replaces, minus the extra dependency.
type boundedPool struct {
	sem chan struct{}
}

func newBoundedPool(n int) *boundedPool {
	return &boundedPool{sem: make(chan struct{}, n)}
}

func (p *boundedPool) Go(fn func()) {
	p.sem <- struct{}{}
	go func() {
		defer func() { <-p.sem }()
		fn()
	}()
}

func (p *boundedPool) Wait() {
	for i := 0; i < cap(p.sem); i++ {
		p.sem <- struct{}{}
	}
}

type ActionResult struct {
	Username  string
	Following int
	Action    string // blocked | would_block | already_blocked | whitelisted | ok | skipped | block_failed
	Reason    string
}

// ProcessFollowers decides and (unless dry-run) executes the action for every
// follower. Decision order: whitelist wins, then blacklist, then already
// blocked (persisted), then the following-count threshold.
func ProcessFollowers(
	ctx context.Context,
	gh *GitHubClient,
	cfg Config,
	store *BlockStore,
	followers []User,
) []ActionResult {
	var mu sync.Mutex
	results := make([]ActionResult, 0, len(followers))

	pool := newBoundedPool(cfg.Concurrency)

	for _, f := range followers {
		username := f.Login

		pool.Go(func() {
			res := ActionResult{Username: username}

			switch {
			case cfg.IsWhitelisted(username):
				res.Action = "whitelisted"
				res.Reason = "whitelist"
			case cfg.IsBlacklisted(username):
				res.Action = blockAction(cfg.DryRun)
				res.Reason = "blacklist"
			case store.Contains(username):
				res.Action = "already_blocked"
				res.Reason = "persisted block"
			default:
				count, err := gh.GetFollowingCount(ctx, username)
				if err != nil {
					log.Printf("failed to get following count for %s: %v", username, err)
					res.Action = "skipped"
					res.Reason = "api error"
					break
				}
				res.Following = count
				if count >= cfg.Threshold {
					res.Action = blockAction(cfg.DryRun)
					res.Reason = fmt.Sprintf("following %d >= threshold %d", count, cfg.Threshold)
				} else {
					res.Action = "ok"
					res.Reason = fmt.Sprintf("following %d < threshold %d", count, cfg.Threshold)
				}
			}

			switch res.Action {
			case "blocked":
				log.Printf("blocking %s: %s", username, res.Reason)
				if err := gh.BlockUser(ctx, username); err != nil {
					log.Printf("block %s failed: %v", username, err)
					res.Action = "block_failed"
					res.Reason = err.Error()
				}
			case "would_block":
				log.Printf("dry run: would block %s: %s", username, res.Reason)
			case "whitelisted", "already_blocked":
				log.Printf("skip %s: %s", username, res.Reason)
			}

			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		})
	}

	pool.Wait()
	return results
}

func blockAction(dryRun bool) string {
	if dryRun {
		return "would_block"
	}
	return "blocked"
}

func countAction(results []ActionResult, action string) int {
	n := 0
	for _, r := range results {
		if r.Action == action {
			n++
		}
	}
	return n
}
