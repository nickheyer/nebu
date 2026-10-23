package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func load(t *testing.T, text string, env map[string]string) (*v1.Config, error) {
	t.Helper()
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, envPrefix+"_") {
			t.Setenv(name, "")
			os.Unsetenv(name)
		}
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	path := ""
	if text != "" {
		path = filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Load(path)
}

func TestDefaultsAndDerivedKeys(t *testing.T) {
	cfg, err := load(t, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GetListen() != "127.0.0.1:8484" || cfg.GetLogging().GetLevel() != "info" || cfg.GetTransfer().GetWorkers() != 8 || cfg.GetGateway().GetPolicy().GetUpstreamTimeoutMs() != 600000 {
		t.Fatalf("defaults %v", cfg)
	}
	if cfg.GetStoreDir() != filepath.Join(cfg.GetDataDir(), "store") || cfg.GetBuilds().GetDir() != filepath.Join(cfg.GetDataDir(), "builds") {
		t.Fatalf("derived dirs %q %q", cfg.GetStoreDir(), cfg.GetBuilds().GetDir())
	}
	if cfg.Mesh.Announce == nil || !cfg.GetMesh().GetAnnounce() || cfg.GetMesh().GetExposure() != "guard" {
		t.Fatalf("mesh defaults %v", cfg.GetMesh())
	}
	if cfg.Auth == nil || cfg.Auth.Oidc == nil || cfg.GetAuth().GetSessionTtl() != "24h" || cfg.GetAuth().GetOidc().GetGroupsClaim() != "groups" {
		t.Fatalf("auth defaults %v", cfg.GetAuth())
	}
	if got := cfg.GetContexts(); len(got) != 3 || got[2] != 131072 {
		t.Fatalf("contexts %v", got)
	}
}

func TestFileOverridesDefaultsAndKeepsSiblings(t *testing.T) {
	text := `
listen: 0.0.0.0:9000
data_dir: /srv/nebu
logging:
  level: debug
builds:
  sandbox: oci
  cli: [docker]
transfer:
  windows:
    - days: mon-fri
      from: "09:00"
      to: "17:00"
      max_bytes_per_second: 1000
      pause: true
sources:
  - id: hf
    kind: huggingface
    config:
      token_env: HF_TOKEN
mesh:
  announce: false
auth:
  oidc:
    issuer: https://accounts.example.com/realm
`
	cfg, err := load(t, text, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GetListen() != "0.0.0.0:9000" || cfg.GetLogging().GetLevel() != "debug" || cfg.GetLogging().GetFormat() != "text" {
		t.Fatalf("overrides %v", cfg)
	}
	if cfg.GetStoreDir() != filepath.Join("/srv/nebu", "store") {
		t.Fatalf("store_dir follows data_dir, got %q", cfg.GetStoreDir())
	}
	if cfg.GetBuilds().GetSandbox() != v1.SandboxKind_SANDBOX_KIND_OCI || len(cfg.GetBuilds().GetCli()) != 1 || cfg.GetBuilds().GetJobs() == 0 {
		t.Fatalf("builds %v", cfg.GetBuilds())
	}
	w := cfg.GetTransfer().GetWindows()
	if len(w) != 1 || w[0].GetDays() != "mon-fri" || w[0].GetFrom() != "09:00" || w[0].GetMaxBytesPerSecond() != 1000 || !w[0].GetPause() || cfg.GetTransfer().GetWorkers() != 8 {
		t.Fatalf("windows %v", cfg.GetTransfer())
	}
	s := cfg.GetSources()
	if len(s) != 1 || s[0].GetId() != "hf" || s[0].GetKind() != v1.SourceKind_SOURCE_KIND_HUGGINGFACE || s[0].GetConfig()["token_env"] != "HF_TOKEN" {
		t.Fatalf("sources %v", s)
	}
	if cfg.Mesh.Announce == nil || cfg.GetMesh().GetAnnounce() {
		t.Fatalf("announce %v", cfg.GetMesh())
	}
	if cfg.GetAuth().GetOidc().GetName() != "accounts.example.com" || cfg.GetAuth().GetOidc().GetScopes()[0] != "openid" {
		t.Fatalf("oidc %v", cfg.GetAuth().GetOidc())
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	env := map[string]string{
		"NEBU_LISTEN":                  "[::]:1234",
		"NEBU_AUTH_TOKEN":              "secret",
		"NEBU_AUTH_OIDC_CLIENT_SECRET": "s3",
		"NEBU_GATEWAY_API_KEYS":        "a, b,c",
		"NEBU_CONTEXTS":                "4096,8192",
		"NEBU_MESH_ANNOUNCE":           "false",
		"NEBU_BUILDS_SANDBOX":          "SANDBOX_KIND_HOST",
		"NEBU_MIN_FREE_BYTES":          "1024",
		"NEBU_LOGGING_LEVEL":           "",
	}
	cfg, err := load(t, "listen: 127.0.0.1:1\nlogging:\n  level: warn\n", env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GetListen() != "[::]:1234" || cfg.GetAuth().GetToken() != "secret" || cfg.GetAuth().GetOidc().GetClientSecret() != "s3" {
		t.Fatalf("env scalars %v", cfg)
	}
	if got := cfg.GetGateway().GetApiKeys(); len(got) != 3 || got[1] != "b" {
		t.Fatalf("api keys %v", got)
	}
	if got := cfg.GetContexts(); len(got) != 2 || got[0] != 4096 {
		t.Fatalf("contexts %v", got)
	}
	if cfg.GetMesh().GetAnnounce() || cfg.GetBuilds().GetSandbox() != v1.SandboxKind_SANDBOX_KIND_HOST || cfg.GetMinFreeBytes() != 1024 {
		t.Fatalf("env typed values %v", cfg)
	}
	if cfg.GetLogging().GetLevel() != "warn" {
		t.Fatalf("an empty variable leaves the file's value, got %q", cfg.GetLogging().GetLevel())
	}
}

func TestRefusals(t *testing.T) {
	cases := map[string]string{
		"listn: x":                       "unknown key listn",
		"auth:\n  oidc:\n    foo: 1":     "unknown key auth.oidc.foo",
		"transfer:\n  workers: many":     "transfer.workers",
		"builds:\n  sandbox: bogus":      `"bogus" is not a SandboxKind`,
		"mesh:\n  announce: maybe":       "mesh.announce",
		"sources:\n  - 3":                "sources[0]",
		"gateway:\n  api_keys: {a: 1}":   "gateway.api_keys",
		"- just\n- a list":               "mapping",
		"transfer:\n  windows: [{x: 1}]": "unknown key transfer.windows[0].x",
	}
	for text, want := range cases {
		_, err := load(t, text, nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want error with %q, got %v", text, want, err)
		}
	}
	if _, err := load(t, "", map[string]string{"NEBU_TRANSFER_WORKERS": "lots"}); err == nil || !strings.Contains(err.Error(), "NEBU_TRANSFER_WORKERS") {
		t.Fatalf("bad env number: %v", err)
	}
	if _, err := load(t, "", map[string]string{"NEBU_SOURCES": "x"}); err == nil || !strings.Contains(err.Error(), "only the config file") {
		t.Fatalf("structured env: %v", err)
	}
}

func TestSearchPathSkipsTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("listen: 127.0.0.1:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	home := t.TempDir()
	t.Setenv("HOME", home)
	system := systemDir
	systemDir = filepath.Join(t.TempDir(), "etc", "nebu")
	t.Cleanup(func() { systemDir = system })
	cfg, err := load(t, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GetListen() == "127.0.0.1:1" {
		t.Fatal("the working directory's config.yaml was read")
	}
	if err := os.MkdirAll(filepath.Join(home, ".config", "nebu"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "nebu", "config.yml"), []byte("listen: 127.0.0.1:2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err = load(t, "", nil); err != nil || cfg.GetListen() != "127.0.0.1:2" {
		t.Fatalf("home config: %v %v", cfg.GetListen(), err)
	}
}
