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
