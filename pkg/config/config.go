// Package config loads daemon and client configuration.
package config

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/spf13/viper"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// API token the daemon generates in data_dir when auth.token is empty
const TokenFile = "api.token"

// Loads the file at path, or the first config.yaml on the search path
func Load(path string) (*v1.Config, error) {
	dataDir, err := dataHome()
	if err != nil {
		return nil, err
	}
	cacheDir, _ := os.UserCacheDir()

	v := viper.New()
	v.SetConfigName("config")
	v.AddConfigPath("/etc/nebu")
	v.AddConfigPath("$HOME/.config/nebu")
	v.AddConfigPath(".")
	if path != "" {
		v.SetConfigFile(path)
	}

	v.SetEnvPrefix("nebu")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetDefault("listen", "127.0.0.1:8484")
	v.SetDefault("addr", "")
	v.SetDefault("data_dir", filepath.Join(dataDir, "nebu"))
	v.SetDefault("cache_dir", filepath.Join(cacheDir, "nebu"))
	v.SetDefault("sources", []any{})
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "text")
	v.SetDefault("logging.file", "")
	v.SetDefault("contexts", []uint32{8192, 32768, 131072})
	v.SetDefault("min_free_bytes", uint64(50<<30))
	v.SetDefault("transfer.workers", 8)
	v.SetDefault("transfer.chunk_bytes", 32<<20)
	v.SetDefault("transfer.retries", 5)
	v.SetDefault("transfer.max_bytes_per_second", 0)
	v.SetDefault("transfer.windows", []any{})
	v.SetDefault("gateway.listen", "")
	v.SetDefault("gateway.api_keys", []string{})
	v.SetDefault("gateway.drain_timeout_ms", 30000)
	v.SetDefault("gateway.policy.max_in_flight", 0)
	v.SetDefault("gateway.policy.requests_per_second", 0.0)
	v.SetDefault("gateway.policy.burst", 0)
	v.SetDefault("gateway.policy.request_timeout_ms", 0)
	v.SetDefault("gateway.policy.upstream_timeout_ms", 600000)
	v.SetDefault("gateway.cors_origins", []string{})
	v.SetDefault("auth.token", "")
	v.SetDefault("auth.oidc.issuer", "")
	v.SetDefault("auth.oidc.client_id", "")
	v.SetDefault("auth.oidc.client_secret", "")
	v.SetDefault("auth.oidc.scopes", []string{"openid", "profile", "email"})
	v.SetDefault("auth.oidc.public_url", "")
	v.SetDefault("auth.oidc.allowed_emails", []string{})
	v.SetDefault("auth.oidc.allowed_domains", []string{})
	v.SetDefault("auth.oidc.allowed_groups", []string{})
	v.SetDefault("auth.oidc.groups_claim", "groups")
	v.SetDefault("auth.disabled", false)
	v.SetDefault("auth.session_ttl", "24h")
	v.SetDefault("builds.sandbox", "unspecified")
	v.SetDefault("builds.image", "")
	v.SetDefault("builds.cli", []string{"podman", "docker", "nerdctl"})
	v.SetDefault("builds.jobs", runtime.NumCPU())
	v.SetDefault("web.disabled", false)
	v.SetDefault("store.max_bytes", 0)
	v.SetDefault("tls.cert_file", "")
	v.SetDefault("tls.key_file", "")
	v.SetDefault("notify.webhooks", []string{})
	v.SetDefault("discord.ffmpeg", "")
	v.SetDefault("mesh.listen", "")
	v.SetDefault("mesh.advertise", "")
	v.SetDefault("mesh.announce", true)
	v.SetDefault("mesh.exposure", "guard")
	v.SetDefault("mesh.ports", "")

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return nil, err
		}
	}

	// Derived from keys the file or environment may have set
	dataDir = v.GetString("data_dir")
	v.SetDefault("store_dir", filepath.Join(dataDir, "store"))
	v.SetDefault("builds.dir", filepath.Join(dataDir, "builds"))
	v.SetDefault("auth.oidc.name", issuerHost(v.GetString("auth.oidc.issuer")))

	cfg := &v1.Config{}
	hooks := viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(mapstructure.StringToSliceHookFunc(","), enumHook))
	if err := v.UnmarshalExact(cfg, hooks, jsonTags); err != nil {
		return nil, fmt.Errorf("%s: %w", cmp.Or(v.ConfigFileUsed(), "config"), err)
	}
	return cfg, nil
}

// Generated messages carry the proto field names in json tags
func jsonTags(c *mapstructure.DecoderConfig) { c.TagName = "json" }

// Decodes enum values written by name, with or without the type prefix
func enumHook(from, to reflect.Type, data any) (any, error) {
	if from.Kind() != reflect.String || to.Kind() != reflect.Int32 {
		return data, nil
	}
	enum, ok := reflect.Zero(to).Interface().(protoreflect.Enum)
	if !ok {
		return data, nil
	}
	name, values := data.(string), enum.Descriptor().Values()
	for i := 0; i < values.Len(); i++ {
		value := values.Get(i)
		if full := string(value.Name()); strings.EqualFold(full, name) || strings.HasSuffix(full, "_"+strings.ToUpper(name)) {
			return reflect.ValueOf(value.Number()).Convert(to).Interface(), nil
		}
	}
	return nil, fmt.Errorf("%q is not a %s", name, enum.Descriptor().Name())
}

// The issuer host, or the issuer itself when it is not a URL
func issuerHost(issuer string) string {
	if u, err := url.Parse(issuer); err == nil && u.Host != "" {
		return u.Hostname()
	}
	return issuer
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
