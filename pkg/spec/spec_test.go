package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/build"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/host/probes"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtime"
	"github.com/nickheyer/nebu/pkg/triage"
	specfs "github.com/nickheyer/nebu/spec"
)

func TestEmbeddedSpecsCompile(t *testing.T) {
	c, err := Load(specfs.FS())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Probes) == 0 || len(c.Formats) == 0 || len(c.Archs) == 0 || len(c.Runtimes) == 0 || len(c.Triage) == 0 || len(c.Recipes) == 0 || len(c.Precisions) == 0 {
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
	if _, err := descriptor.New(c.Formats, c.Archs, c.Precisions); err != nil {
		t.Error(err)
	}
	if _, err := runtime.New(c.Runtimes); err != nil {
		t.Error(err)
	}
	if _, err := triage.New(c.Triage); err != nil {
		t.Error(err)
	}
	ids := map[string]bool{}
	for _, tr := range c.Triage {
		ids[tr.GetId()] = true
	}
	recipes, err := build.New(c.Recipes)
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range c.Runtimes {
		for _, id := range rt.GetTriage() {
			if !ids[id] {
				t.Errorf("runtime %s references missing triage set %s", rt.GetId(), id)
			}
		}
		if id := rt.GetAcquire().GetRecipeId(); id != "" {
			if rc, err := recipes.Get(id); err != nil {
				t.Errorf("runtime %s references missing recipe %s", rt.GetId(), id)
			} else if rc.Spec.GetRuntimeId() != rt.GetId() {
				t.Errorf("recipe %s builds %s, not %s", id, rc.Spec.GetRuntimeId(), rt.GetId())
			}
		}
	}
	for _, rc := range c.Recipes {
		if _, err := runtime.New(c.Runtimes); err == nil {
			found := false
			for _, rt := range c.Runtimes {
				if rt.GetId() == rc.GetRuntimeId() {
					found = true
				}
			}
			if !found {
				t.Errorf("recipe %s builds unknown runtime %s", rc.GetId(), rc.GetRuntimeId())
			}
		}
		for _, p := range rc.GetPatches() {
			if p.GetFile() != "" {
				if _, ok := c.Patches[p.GetFile()]; !ok {
					t.Errorf("recipe %s patch %s references missing file %s", rc.GetId(), p.GetId(), p.GetFile())
				}
			}
		}
	}
	for name := range c.Patches {
		if strings.HasPrefix(name, ".") {
			t.Errorf("dotfile %s loaded as a patch", name)
		}
	}
}

// Every shipped recipe selects a variant on representative hosts
func TestRecipesSelectOnHosts(t *testing.T) {
	c, err := Load(specfs.FS())
	if err != nil {
		t.Fatal(err)
	}
	recipes, err := build.New(c.Recipes)
	if err != nil {
		t.Fatal(err)
	}
	hosts := []*v1.HostProfile{
		{Os: "linux", Arch: "amd64", Facts: map[string]string{"os": "linux", "arch": "amd64"}},
		{Os: "linux", Arch: "amd64", Facts: map[string]string{"os": "linux", "arch": "amd64"}, Devices: []*v1.Device{{Id: "g", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "nvidia", Facts: map[string]string{"compute_capability": "8.6", "index": "0"}}}},
		{Os: "linux", Arch: "amd64", Facts: map[string]string{"os": "linux", "arch": "amd64"}, Devices: []*v1.Device{{Id: "g", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "amd"}}},
		{Os: "darwin", Arch: "arm64", Facts: map[string]string{"os": "darwin", "arch": "arm64"}},
	}
	for _, rc := range recipes.List() {
		for _, h := range hosts {
			sel, err := rc.Select(h, build.Options{Sandbox: v1.SandboxKind_SANDBOX_KIND_OCI, Image: "img", Defaults: &v1.Builds{Cli: []string{"sh"}}})
			if err != nil {
				t.Errorf("recipe %s on %s/%s: %v", rc.Spec.GetId(), h.GetOs(), h.GetArch(), err)
				continue
			}
			if sel.Variant.GetId() == "" {
				t.Errorf("recipe %s selected no variant", rc.Spec.GetId())
			}
			for k, v := range sel.Vars {
				if strings.Contains(v, "{{") {
					t.Errorf("recipe %s var %s not rendered: %q", rc.Spec.GetId(), k, v)
				}
			}
		}
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
