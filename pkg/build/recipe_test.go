package build

import (
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/spec"
)

const testRecipe = `id: fake
runtime_id: fake
source:
  ref: '1.0'
  archive: http://example/fake-{{.ref}}.tar.gz
tools: [sh]
facts: [cpu.count, device.vendor, device.compute_capability]
vars:
  stamp: 'v{{.ref}}-{{.variant}}'
  extra: ''
variants:
  - id: gpu
    when: any(devices, .kind == "gpu")
    tools: [definitely-missing-tool-xyz]
    vars:
      backend: gpu-{{.vars.stamp}}
  - id: cpu
    when: 'true'
    vars:
      backend: cpu
steps:
  - name: one
    command: [sh, -c, 'true']
outputs: [bin]
binary: bin
`

func recipe(t *testing.T, yaml string) *Recipe {
	t.Helper()
	msg := &v1.Recipe{}
	if err := spec.Decode([]byte(yaml), msg); err != nil {
		t.Fatal(err)
	}
	reg, err := New([]*v1.Recipe{msg})
	if err != nil {
		t.Fatal(err)
	}
	rc, err := reg.Get(msg.GetId())
	if err != nil {
		t.Fatal(err)
	}
	return rc
}

func gpuProfile() *v1.HostProfile {
	return &v1.HostProfile{
		Os: "linux", Arch: "amd64",
		Facts:   map[string]string{"cpu.count": "8", "os": "linux", "arch": "amd64"},
		Devices: []*v1.Device{{Id: "g0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "acme", Facts: map[string]string{"compute_capability": "8.6"}}, {Id: "g1", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "acme", Facts: map[string]string{"compute_capability": "7.5"}}},
	}
}

func TestSelectSkipsVariantMissingTools(t *testing.T) {
	rc := recipe(t, testRecipe)
	sel, err := rc.Select(gpuProfile(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Variant.GetId() != "cpu" {
		t.Fatalf("variant %s, gpu needs a missing tool", sel.Variant.GetId())
	}
	if sel.Vars["stamp"] != "v1.0-cpu" || sel.Vars["backend"] != "cpu" {
		t.Fatalf("vars %v", sel.Vars)
	}
	if sel.Facts["cpu.count"] != "8" || sel.Facts["device.vendor"] != "acme,acme" || sel.Facts["device.compute_capability"] != "7.5,8.6" {
		t.Fatalf("facts %v", sel.Facts)
	}
	if sel.Err() != nil || len(sel.Unmet) != 0 {
		t.Fatalf("unmet %v", sel.Unmet)
	}
}

func TestSelectExplicitVariantReportsTools(t *testing.T) {
	rc := recipe(t, testRecipe)
	sel, err := rc.Select(gpuProfile(), Options{Variant: "gpu", Vars: map[string]string{"extra": "-DX=1"}})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Variant.GetId() != "gpu" || sel.Vars["backend"] != "gpu-v1.0-gpu" || sel.Vars["extra"] != "-DX=1" {
		t.Fatalf("selection %s %v", sel.Variant.GetId(), sel.Vars)
	}
	if sel.Err() == nil || len(sel.MissingTools) != 1 {
		t.Fatalf("missing tool should be unmet: %v", sel.Unmet)
	}
	if _, err := rc.Select(gpuProfile(), Options{Variant: "nope"}); err == nil {
		t.Fatal("unknown variant should fail")
	}
	st := sel.Status()
	if st.GetVariant() != "gpu" || len(st.GetUnmet()) != 1 || st.GetSandbox() != v1.SandboxKind_SANDBOX_KIND_HOST {
		t.Fatalf("status %v", st)
	}
}

func TestSelectOCI(t *testing.T) {
	rc := recipe(t, testRecipe)
	sel, err := rc.Select(gpuProfile(), Options{Sandbox: v1.SandboxKind_SANDBOX_KIND_OCI, Image: "img:1", Defaults: &v1.Builds{Cli: []string{"sh"}}})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Variant.GetId() != "gpu" {
		t.Fatalf("tools are not checked for containers, got %s", sel.Variant.GetId())
	}
	if sel.CLI == "" || sel.Image != "img:1" || sel.Err() != nil {
		t.Fatalf("oci selection cli=%q image=%q unmet=%v", sel.CLI, sel.Image, sel.Unmet)
	}
	sel, err = rc.Select(gpuProfile(), Options{Sandbox: v1.SandboxKind_SANDBOX_KIND_OCI, Defaults: &v1.Builds{Cli: []string{"no-such-cli-abc"}}})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Err() == nil || len(sel.Unmet) != 2 {
		t.Fatalf("missing cli and image should both be unmet: %v", sel.Unmet)
	}
}

func TestCompileErrors(t *testing.T) {
	bad := []string{
		"id: x\nruntime_id: r\nbinary: b\n",
		"id: x\nruntime_id: r\nsteps: [{command: [a]}]\n",
		"id: x\nbinary: b\nsteps: [{command: [a]}]\n",
		"id: x\nruntime_id: r\nbinary: b\nsteps: [{command: ['{{.broken']}]\n",
		"id: x\nruntime_id: r\nbinary: b\nsteps: [{command: [a]}]\nvariants: [{id: v, when: 'this is not ok('}]\n",
		"id: x\nruntime_id: r\nbinary: b\nsteps: [{command: [a]}]\npatches: [{id: p}]\n",
	}
	for _, y := range bad {
		msg := &v1.Recipe{}
		if err := spec.Decode([]byte(y), msg); err != nil {
			t.Fatal(err)
		}
		if _, err := New([]*v1.Recipe{msg}); err == nil {
			t.Errorf("should fail: %s", strings.ReplaceAll(y, "\n", " "))
		}
	}
	msg := &v1.Recipe{}
	spec.Decode([]byte(testRecipe), msg)
	if _, err := New([]*v1.Recipe{msg, msg}); err == nil {
		t.Fatal("duplicate id should fail")
	}
}

func TestHashChangesWithInputs(t *testing.T) {
	rc := recipe(t, testRecipe)
	base := func() *v1.Build {
		return &v1.Build{Variant: "cpu", Ref: "1.0", Sandbox: v1.SandboxKind_SANDBOX_KIND_HOST, Vars: map[string]string{"a": "1"}, Facts: map[string]string{"f": "x"}, Patches: []string{"p"}}
	}
	patches := map[string][]byte{"p": []byte("diff")}
	id := hashBuild(base(), rc.Spec, patches)
	if len(id) != idLength || id != hashBuild(base(), rc.Spec, patches) {
		t.Fatal("hash should be stable")
	}
	for name, mutate := range map[string]func(*v1.Build){
		"ref":     func(b *v1.Build) { b.Ref = "2.0" },
		"variant": func(b *v1.Build) { b.Variant = "gpu" },
		"vars":    func(b *v1.Build) { b.Vars["a"] = "2" },
		"facts":   func(b *v1.Build) { b.Facts["f"] = "y" },
		"sandbox": func(b *v1.Build) { b.Sandbox = v1.SandboxKind_SANDBOX_KIND_OCI },
		"image":   func(b *v1.Build) { b.Image = "img" },
		"patches": func(b *v1.Build) { b.Patches = nil },
	} {
		b := base()
		mutate(b)
		if hashBuild(b, rc.Spec, patches) == id {
			t.Errorf("%s change should change the hash", name)
		}
	}
	if hashBuild(base(), rc.Spec, map[string][]byte{"p": []byte("other diff")}) == id {
		t.Error("patch content change should change the hash")
	}
}

func TestRegistryForRuntime(t *testing.T) {
	rc := recipe(t, testRecipe)
	reg, _ := New([]*v1.Recipe{rc.Spec})
	if len(reg.ForRuntime("fake")) != 1 || len(reg.ForRuntime("other")) != 0 || len(reg.List()) != 1 {
		t.Fatal("registry lookups")
	}
	if _, err := reg.Get("missing"); err == nil {
		t.Fatal("missing recipe")
	}
	if rc.Timeout() != DefaultTimeout {
		t.Fatal("default timeout")
	}
}
