package spec

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/host/probes"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtime"
	specfs "github.com/nickheyer/nebu/spec"
)

func TestEmbeddedSpecsCompile(t *testing.T) {
	c, err := Load(specfs.FS())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Probes) == 0 || len(c.Formats) == 0 || len(c.Archs) == 0 || len(c.Runtimes) == 0 {
		t.Fatalf("catalog incomplete: %+v", c)
	}
	for _, p := range c.Probes {
		if _, err := probes.Compile(p); err != nil {
			t.Error(err)
		}
	}
	if _, err := formats.NewClassifier(c.Formats); err != nil {
		t.Error(err)
	}
	if _, err := descriptor.New(c.Formats, c.Archs); err != nil {
		t.Error(err)
	}
	if _, err := runtime.New(c.Runtimes); err != nil {
		t.Error(err)
	}
}

func TestLayeredOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "probes"), 0o755); err != nil {
		t.Fatal(err)
	}
	override := "id: nvidia-smi\nexec:\n  file: /nonexistent\nparse:\n  kind: PARSE_KIND_CSV\n"
	if err := os.WriteFile(filepath.Join(dir, "probes", "z.yaml"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	base, err := Load(specfs.FS())
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(specfs.FS(), os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Probes) != len(base.Probes) {
		t.Fatalf("override should replace, got %d want %d", len(c.Probes), len(base.Probes))
	}
	for _, p := range c.Probes {
		if p.GetId() == "nvidia-smi" && p.GetExec().GetFile() != "/nonexistent" {
			t.Fatal("override not applied")
		}
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	if err := Decode([]byte("id: x\nbogus: 1\n"), &v1.ProbeSpec{}); err == nil {
		t.Fatal("unknown field should fail")
	}
}

var denied = regexp.MustCompile(`(?i)\b(nvidia|rocm|cuda|deepseek|qwen|llama|mistral|gemma|vllm|amd|intel|apple|hopper|blackwell)\b`)

func TestNoOneOffsInGo(t *testing.T) {
	root := filepath.Join("..", "..")
	skip := map[string]bool{"spec": true, "test": true, "web": true, ".git": true}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if info.IsDir() {
			if skip[rel] || rel == filepath.Join("pkg", "proto") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if m := denied.FindString(string(data)); m != "" {
			t.Errorf("%s mentions %q, move it into spec data", rel, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
