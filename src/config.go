package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const (
	defaultThreshold   = 20000
	defaultConcurrency = 10
	defaultTimeoutSec  = 60
	defaultDataDir     = "data"
	defaultMaxReports  = 5
)

type Config struct {
	Username      string       `json:"username"`
	Threshold     int          `json:"threshold"`
	WhitelistFile string       `json:"whitelist_file"`
	BlacklistFile string       `json:"blacklist_file"`
	Concurrency   int          `json:"concurrency"`
	TimeoutSec    int          `json:"timeout_sec"`
	DryRun        bool         `json:"dry_run"`
	DataDir       string       `json:"data_dir"`
	Report        ReportConfig `json:"report"`
	Schedule      Schedule     `json:"schedule"`

	PAT        string // injected from GH_PAT env, never read from the config file
	whitelistL map[string]struct{}
	blacklistL map[string]struct{}
}

type ReportConfig struct {
	Issue      bool   `json:"issue"`
	IssueRepo  string `json:"issue_repo"`
	MaxReports int    `json:"max_reports"`
}

// Schedule is informational: GitHub Actions only honors the cron inside the
// workflow file, so keep config.json's schedule.cron in sync with the cron in
// .github/workflows/antibot.yaml.
type Schedule struct {
	Cron string `json:"cron"`
}

func LoadConfig(path, pat string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.finalize(pat); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c *Config) finalize(pat string) error {
	c.Username = strings.TrimSpace(c.Username)
	if c.Username == "" {
		return fmt.Errorf("username is required in config")
	}
	if pat == "" {
		return fmt.Errorf("GH_PAT is required")
	}
	c.PAT = pat
	if c.Threshold <= 0 {
		c.Threshold = defaultThreshold
	}
	if c.Concurrency <= 0 {
		c.Concurrency = defaultConcurrency
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = defaultTimeoutSec
	}
	if c.DataDir == "" {
		c.DataDir = defaultDataDir
	}
	if c.Report.MaxReports <= 0 {
		c.Report.MaxReports = defaultMaxReports
	}

	var err error
	if c.whitelistL, err = loadListFile(c.WhitelistFile); err != nil {
		return fmt.Errorf("load whitelist: %w", err)
	}
	if c.blacklistL, err = loadListFile(c.BlacklistFile); err != nil {
		return fmt.Errorf("load blacklist: %w", err)
	}
	return nil
}

// loadListFile reads one username per line from path. Missing file means an
// empty list; "#" starts a comment. GitHub usernames are case-insensitive, so
// entries are normalized to lowercase.
func loadListFile(path string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	if path == "" {
		return out, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[strings.ToLower(line)] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (c Config) IsWhitelisted(username string) bool {
	_, ok := c.whitelistL[strings.ToLower(username)]
	return ok
}

func (c Config) IsBlacklisted(username string) bool {
	_, ok := c.blacklistL[strings.ToLower(username)]
	return ok
}
