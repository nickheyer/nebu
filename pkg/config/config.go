// Package config loads daemon and client configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"sigs.k8s.io/yaml"
)

const (
	// Environment variable naming the config file
	envConfig = "NEBU_CONFIG"
	// Environment variable naming the daemon address
	envAddr = "NEBU_ADDR"
	// Environment variable naming the data directory
	envDataDir = "NEBU_DATA_DIR"
	// Environment variable naming the listen address
	envListen = "NEBU_LISTEN"
	// Environment variable holding the API token
	envToken = "NEBU_TOKEN"
	// Comma separated gateway keys read when gateway.api_key_env is unset
	envAPIKeys = "NEBU_API_KEYS"
	// Single sign-on provider settings
	envOIDCIssuer   = "NEBU_OIDC_ISSUER"
	envOIDCClientID = "NEBU_OIDC_CLIENT_ID"
	// Client secret read when auth.oidc.client_secret_env is unset
	envOIDCSecret = "NEBU_OIDC_CLIENT_SECRET"

	// API token the daemon generates in data_dir when auth.token is empty
	TokenFile = "api.token"

	defaultListen  = "127.0.0.1:8484"
	minFreeBytes   = 50 << 30
	workers        = 8
	chunkBytes     = 32 << 20
	retries        = 5
	drainTimeoutMs = 30000
	// Runtime readiness timeout.
	upstreamTimeoutMs = 600000
	sessionTTL        = "24h"
	groupsClaim       = "groups"
)

// Scopes requested from the provider when auth.oidc.scopes is empty
var defaultScopes = []string{"openid", "profile", "email"}

// Container CLIs tried in order when config names none
var defaultCLIs = []string{"podman", "docker", "nerdctl"}

// Search order when no path is given
func candidates() []string {
	var out []string
	if p := os.Getenv(envConfig); p != "" {
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
		if err := Decode(data, cfg); err != nil {
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

// Decodes a YAML or JSON config file into the config message, refusing keys it does not know
func Decode(data []byte, cfg *v1.Config) error {
	js, err := yaml.YAMLToJSON(data)
	if err != nil {
		return fmt.Errorf("yaml: %w", err)
	}
	if err := protojson.Unmarshal(js, cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	return nil
}

func applyEnv(cfg *v1.Config) {
	if v := os.Getenv(envAddr); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv(envDataDir); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv(envListen); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv(envToken); v != "" {
		if cfg.Auth == nil {
			cfg.Auth = &v1.Auth{}
		}
		cfg.Auth.Token = v
	}
	issuer, clientID := os.Getenv(envOIDCIssuer), os.Getenv(envOIDCClientID)
	if issuer != "" || clientID != "" {
		if cfg.Auth == nil {
			cfg.Auth = &v1.Auth{}
		}
		if cfg.Auth.Oidc == nil {
			cfg.Auth.Oidc = &v1.Oidc{}
		}
		if issuer != "" {
			cfg.Auth.Oidc.Issuer = issuer
		}
		if clientID != "" {
			cfg.Auth.Oidc.ClientId = clientID
		}
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
	if cfg.Gateway == nil {
		cfg.Gateway = &v1.Gateway{}
	}
	if cfg.Gateway.DrainTimeoutMs == 0 {
		cfg.Gateway.DrainTimeoutMs = drainTimeoutMs
	}
	if cfg.Gateway.Policy == nil {
		cfg.Gateway.Policy = &v1.Policy{}
	}
	if cfg.Gateway.Policy.UpstreamTimeoutMs == 0 {
		cfg.Gateway.Policy.UpstreamTimeoutMs = upstreamTimeoutMs
	}
	if cfg.Auth == nil {
		cfg.Auth = &v1.Auth{}
	}
	if cfg.Auth.Token == "" && cfg.Auth.TokenEnv != "" {
		cfg.Auth.Token = os.Getenv(cfg.Auth.TokenEnv)
	}
	if cfg.Auth.SessionTtl == "" {
		cfg.Auth.SessionTtl = sessionTTL
	}
	// The daemon writes the token it generated, so clients on the same host find it
	if cfg.Auth.Token == "" && !cfg.Auth.Disabled {
		if data, err := os.ReadFile(filepath.Join(cfg.DataDir, TokenFile)); err == nil {
			cfg.Auth.Token = strings.TrimSpace(string(data))
		}
	}
	if o := cfg.Auth.Oidc; o != nil && o.Issuer != "" {
		if o.ClientSecretEnv == "" {
			o.ClientSecretEnv = envOIDCSecret
		}
		if o.ClientSecret == "" {
			o.ClientSecret = os.Getenv(o.ClientSecretEnv)
		}
		if len(o.Scopes) == 0 {
			o.Scopes = append([]string(nil), defaultScopes...)
		}
		if o.GroupsClaim == "" {
			o.GroupsClaim = groupsClaim
		}
		if o.Name == "" {
			o.Name = issuerName(o.Issuer)
		}
	}
	if cfg.Gateway.ApiKeyEnv == "" {
		cfg.Gateway.ApiKeyEnv = envAPIKeys
	}
	if cfg.Gateway.ApiKeyEnv != "" {
		if v := os.Getenv(cfg.Gateway.ApiKeyEnv); v != "" {
			cfg.Gateway.ApiKeys = append(cfg.Gateway.ApiKeys, splitKeys(v)...)
		}
	}
	if cfg.Builds == nil {
		cfg.Builds = &v1.Builds{}
	}
	if cfg.Builds.Dir == "" {
		cfg.Builds.Dir = filepath.Join(cfg.DataDir, "builds")
	}
	if len(cfg.Builds.Cli) == 0 {
		cfg.Builds.Cli = append([]string(nil), defaultCLIs...)
	}
	if cfg.Builds.Jobs == 0 {
		cfg.Builds.Jobs = uint32(runtime.NumCPU())
	}
	if cfg.Web == nil {
		cfg.Web = &v1.Web{}
	}
	return nil
}

// The issuer's host, or the issuer itself when it is not a URL
func issuerName(issuer string) string {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return issuer
	}
	return u.Hostname()
}

// Splits a comma or whitespace separated key list
func splitKeys(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Where the platform keeps application data, XDG on unix
func dataHome() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return v, nil
		}
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support"), nil
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}
