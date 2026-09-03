// Package config loads daemon and client configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/spec"
)

const (
	// Environment variable naming the config file
	EnvConfig = "NEBU_CONFIG"
	// Environment variable naming the daemon address
	EnvAddr = "NEBU_ADDR"
	// Environment variable naming the data directory
	EnvDataDir = "NEBU_DATA_DIR"
	// Environment variable naming the listen address
	EnvListen = "NEBU_LISTEN"

	defaultListen = "127.0.0.1:8484"
	defaultSource = "huggingface"
	minFreeBytes  = 50 << 30
	workers       = 8
	chunkBytes    = 32 << 20
	retries       = 5
)

// Search order when no path is given
func candidates() []string {
	var out []string
	if p := os.Getenv(EnvConfig); p != "" {
		out = append(out, p)
	}
	out = append(out, "nebu.yaml")
	if dir, err := os.UserConfigDir(); err == nil {
		out = append(out, filepath.Join(dir, "nebu", "config.yaml"))
	}
	return append(out, "/etc/nebu/config.yaml")
}

// Loads config from path or the first candidate found
func Load(path string) (*v1.Config, error) {
	cfg := &v1.Config{}
	paths := candidates()
	if path != "" {
		paths = []string{path}
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			if path != "" {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := spec.Decode(data, cfg); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		break
	}
	applyEnv(cfg)
	if err := applyDefaults(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyEnv(cfg *v1.Config) {
	if v := os.Getenv(EnvAddr); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv(EnvDataDir); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv(EnvListen); v != "" {
		cfg.Listen = v
	}
}

func applyDefaults(cfg *v1.Config) error {
	if cfg.Listen == "" {
		cfg.Listen = defaultListen
	}
	if cfg.DataDir == "" {
		base, err := dataHome()
		if err != nil {
			return err
		}
		cfg.DataDir = filepath.Join(base, "nebu")
	}
	if cfg.CacheDir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			base = filepath.Join(cfg.DataDir, "cache")
		} else {
			base = filepath.Join(base, "nebu")
		}
		cfg.CacheDir = base
	}
	if cfg.Logging == nil {
		cfg.Logging = &v1.Logging{}
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "text"
	}
	if len(cfg.Sources) == 0 {
		cfg.Sources = []*v1.Source{{Id: defaultSource, Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE}}
	}
	if len(cfg.Contexts) == 0 {
		cfg.Contexts = []uint32{8192, 32768, 131072}
	}
	if cfg.MinFreeBytes == 0 {
		cfg.MinFreeBytes = minFreeBytes
	}
	if cfg.StoreDir == "" {
		cfg.StoreDir = filepath.Join(cfg.DataDir, "store")
	}
	if cfg.Transfer == nil {
		cfg.Transfer = &v1.Transfer{}
	}
	if cfg.Transfer.Workers == 0 {
		cfg.Transfer.Workers = workers
	}
	if cfg.Transfer.ChunkBytes == 0 {
		cfg.Transfer.ChunkBytes = chunkBytes
	}
	if cfg.Transfer.Retries == 0 {
		cfg.Transfer.Retries = retries
	}
	cfg.SpecDirs = append(cfg.SpecDirs, filepath.Join(cfg.DataDir, "spec"))
	return nil
}

func dataHome() (string, error) {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}
