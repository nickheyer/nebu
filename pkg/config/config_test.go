package config

import (
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestLoadDefaultsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nebu.yaml")
	os.WriteFile(path, []byte("data_dir: "+dir+"/data\nsources:\n  - id: local\n    kind: SOURCE_KIND_LOCAL\n    config:\n      path: /models\n"), 0o644)
	t.Setenv(EnvAddr, "127.0.0.1:1")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GetAddr() != "127.0.0.1:1" || cfg.GetListen() == "" || cfg.GetCacheDir() == "" || len(cfg.GetContexts()) == 0 || cfg.GetMinFreeBytes() == 0 {
		t.Fatalf("cfg %+v", cfg)
	}
	if cfg.GetStoreDir() != filepath.Join(dir, "data", "store") || cfg.GetTransfer().GetWorkers() == 0 || cfg.GetTransfer().GetChunkBytes() == 0 {
		t.Fatalf("store defaults %+v", cfg)
	}
	if len(cfg.GetSources()) != 1 || cfg.GetSources()[0].GetKind() != v1.SourceKind_SOURCE_KIND_LOCAL || cfg.GetSources()[0].GetConfig()["path"] != "/models" {
		t.Fatalf("sources %v", cfg.GetSources())
	}
	if cfg.GetSpecDirs()[len(cfg.GetSpecDirs())-1] != filepath.Join(dir, "data", "spec") {
		t.Fatalf("spec dirs %v", cfg.GetSpecDirs())
	}
	if _, err := Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("explicit missing file should fail")
	}
	t.Setenv(EnvConfig, filepath.Join(dir, "also-missing.yaml"))
	cfg, err = Load("")
	// Sources stay empty here, every implemented kind is built and config only overrides or adds
	if err != nil || len(cfg.GetSources()) != 0 || cfg.GetListen() == "" {
		t.Fatalf("defaults %+v %v", cfg, err)
	}
}
