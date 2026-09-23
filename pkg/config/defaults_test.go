package config

import (
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestMilestoneDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nebu.yaml")
	os.WriteFile(path, []byte("data_dir: "+dir+"\ngateway:\n  api_key_env: NEBU_TEST_KEYS\nauth:\n  token_env: NEBU_TEST_TOKEN\n"), 0o644)
	t.Setenv("NEBU_TEST_KEYS", "k1, k2\nk3")
	t.Setenv("NEBU_TEST_TOKEN", "tok")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GetAuth().GetToken() != "tok" {
		t.Fatalf("token from env: %q", cfg.GetAuth().GetToken())
	}
	if got := cfg.GetGateway().GetApiKeys(); len(got) != 3 || got[2] != "k3" {
		t.Fatalf("keys %v", got)
	}
	if cfg.GetGateway().GetDrainTimeoutMs() != drainTimeoutMs {
		t.Fatal("drain default missing")
	}
	if cfg.GetBuilds().GetDir() != filepath.Join(dir, "builds") || len(cfg.GetBuilds().GetCli()) != 3 || cfg.GetBuilds().GetJobs() == 0 {
		t.Fatalf("build defaults %v", cfg.GetBuilds())
	}
	if cfg.GetBuilds().GetSandbox() != v1.SandboxKind_SANDBOX_KIND_UNSPECIFIED || cfg.GetWeb() == nil {
		t.Fatal("sandbox should stay unspecified, web should exist")
	}
	t.Setenv(envToken, "override")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GetAuth().GetToken() != "override" {
		t.Fatal("NEBU_TOKEN should override")
	}
}

func TestOidcEnvAndDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nebu.yaml")
	os.WriteFile(path, []byte("data_dir: "+dir+"\n"), 0o644)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GetAuth().GetOidc() != nil {
		t.Fatal("oidc should stay unset without config or env")
	}
	t.Setenv(envOIDCIssuer, "https://accounts.google.com")
	t.Setenv(envOIDCClientID, "cid")
	t.Setenv(envOIDCSecret, "from-env")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	o := cfg.GetAuth().GetOidc()
	if o.GetIssuer() != "https://accounts.google.com" || o.GetClientId() != "cid" || o.GetClientSecret() != "from-env" || o.GetClientSecretEnv() != envOIDCSecret {
		t.Fatalf("env oidc %v", o)
	}
	if len(o.GetScopes()) != 3 || o.GetGroupsClaim() != "groups" || cfg.GetAuth().GetSessionTtl() != "24h" || o.GetName() != "accounts.google.com" {
		t.Fatalf("oidc defaults %v %v", o, cfg.GetAuth())
	}
	os.WriteFile(path, []byte("data_dir: "+dir+"\nauth:\n  session_ttl: 8h\n  oidc:\n    issuer: https://kc.example/realms/r\n    client_id: nebu\n    client_secret_env: KC_SECRET\n    scopes: [openid, groups]\n    name: Keycloak\n"), 0o644)
	t.Setenv("KC_SECRET", "kc")
	t.Setenv(envOIDCIssuer, "")
	t.Setenv(envOIDCClientID, "")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	o = cfg.GetAuth().GetOidc()
	if o.GetIssuer() != "https://kc.example/realms/r" || o.GetClientSecret() != "kc" || len(o.GetScopes()) != 2 || o.GetName() != "Keycloak" || cfg.GetAuth().GetSessionTtl() != "8h" {
		t.Fatalf("file oidc %v", o)
	}
}

func TestGeneratedTokenIsReadFromDataDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nebu.yaml")
	os.WriteFile(path, []byte("data_dir: "+dir+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, TokenFile), []byte("generated-token\n"), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GetAuth().GetToken() != "generated-token" || cfg.GetAuth().GetDisabled() {
		t.Fatalf("token from data dir %v", cfg.GetAuth())
	}
	t.Setenv(envToken, "explicit")
	if cfg, _ = Load(path); cfg.GetAuth().GetToken() != "explicit" {
		t.Fatal("a configured token wins over the generated file")
	}
	t.Setenv(envToken, "")
	os.WriteFile(path, []byte("data_dir: "+dir+"\nauth:\n  disabled: true\n"), 0o644)
	if cfg, _ = Load(path); cfg.GetAuth().GetToken() != "" || !cfg.GetAuth().GetDisabled() {
		t.Fatalf("disabled auth reads no token %v", cfg.GetAuth())
	}
}
