// Package cache stores small blobs on disk keyed by string.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Disk cache rooted at one directory
type Store struct {
	dir string
}

// Opens or creates a cache directory
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Returns cached bytes when younger than ttl
func (s *Store) Get(key string, ttl time.Duration) ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	p := s.path(key)
	info, err := os.Stat(p)
	if err != nil || (ttl > 0 && time.Since(info.ModTime()) > ttl) {
		return nil, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	return data, true
}

// Writes bytes atomically under key
func (s *Store) Put(key string, data []byte) error {
	if s == nil {
		return nil
	}
	p := s.path(key)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Removes one key, ignoring absence
func (s *Store) Delete(key string) error {
	if s == nil {
		return nil
	}
	err := os.Remove(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:]))
}
