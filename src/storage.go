package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type BlockedEntry struct {
	Username  string `json:"username"`
	BlockedAt string `json:"blocked_at"`
	Reason    string `json:"reason"`
	Following int    `json:"following"`
}

// BlockStore persists every blocked user so past actions are auditable and
// already-blocked users are skipped without repeat lookups. Stored as
// data/blocked.json and committed back to the repository after each run.
type BlockStore struct {
	mu      sync.Mutex
	entries map[string]BlockedEntry // key: lowercase username
	dirty   bool
}

func NewBlockStore() *BlockStore {
	return &BlockStore{entries: map[string]BlockedEntry{}}
}

func LoadBlockStore(path string) (*BlockStore, error) {
	s := NewBlockStore()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &s.entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.entries == nil {
		s.entries = map[string]BlockedEntry{}
	}
	return s, nil
}

func (s *BlockStore) Contains(username string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entries[strings.ToLower(username)]
	return ok
}

// Add records a block; returns false if the user was already recorded.
func (s *BlockStore) Add(username string, following int, reason string) (new bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(username)
	if _, ok := s.entries[key]; ok {
		return false
	}
	s.entries[key] = BlockedEntry{
		Username:  username,
		BlockedAt: time.Now().UTC().Format(time.RFC3339),
		Reason:    reason,
		Following: following,
	}
	s.dirty = true
	return true
}

func (s *BlockStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Save writes the store only when it changed since load; a no-op otherwise.
func (s *BlockStore) Save(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	s.dirty = false
	return nil
}
