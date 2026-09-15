package build

import (
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/recipes"
)

func gpuProfile() *v1.HostProfile {
	return &v1.HostProfile{
		Os: "linux", Arch: "amd64",
		Facts:   map[string]string{"cpu.count": "8", "os": "linux", "arch": "amd64"},
		Devices: []*v1.Device{{Id: "g0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "acme", Facts: map[string]string{"compute_capability": "8.6"}}, {Id: "g1", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "acme", Facts: map[string]string{"compute_capability": "7.5"}}},
	}
}

func TestSelectSkipsVariantMissingTools(t *testing.T) {
	rc := selectRecipe()
	sel, err := Select(rc, gpuProfile(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Variant.ID != "cpu" {
		t.Fatalf("variant %s, gpu needs a missing tool", sel.Variant.ID)
	}
	if sel.Vars["backend"] != "cpu" || sel.Vars["extra"] != "" || sel.Ref != Latest {
		t.Fatalf("vars %v ref %q", sel.Vars, sel.Ref)
	}
	if sel.Facts["cpu.count"] != "8" || sel.Facts["device.vendor"] != "acme,acme" || sel.Facts["device.compute_capability"] != "7.5,8.6" {
		t.Fatalf("facts %v", sel.Facts)
	}
	if sel.Err() != nil || len(sel.Unmet) != 0 {
		t.Fatalf("unmet %v", sel.Unmet)
	}
}

func TestSelectExplicitVariantReportsTools(t *testing.T) {
	rc := selectRecipe()
	sel, err := Select(rc, gpuProfile(), Options{Variant: "gpu", Vars: map[string]string{"extra": "-DX=1"}})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Variant.ID != "gpu" || sel.Vars["backend"] != "gpu" || sel.Vars["extra"] != "-DX=1" {
		t.Fatalf("selection %s %v", sel.Variant.ID, sel.Vars)
	}
	if sel.Err() == nil || len(sel.MissingTools) != 1 {
		t.Fatalf("missing tool should be unmet: %v", sel.Unmet)
	}
	if _, err := Select(rc, gpuProfile(), Options{Variant: "nope"}); err == nil {
		t.Fatal("unknown variant should fail")
	}
	st := sel.Status()
	if st.GetVariant() != "gpu" || len(st.GetUnmet()) != 1 || st.GetSandbox() != v1.SandboxKind_SANDBOX_KIND_HOST || st.GetRecipe().GetSource() != "github.com/o/r" {
		t.Fatalf("status %v", st)
	}
	// A recipe without variants takes the default one and refuses any other name
	plain := selectRecipe()
	plain.variants = nil
	if sel, err := Select(plain, gpuProfile(), Options{}); err != nil || sel.Variant.ID != DefaultVariant {
		t.Fatalf("default variant %v %v", sel, err)
	}
	if _, err := Select(plain, gpuProfile(), Options{Variant: "gpu"}); err == nil {
		t.Fatal("a recipe without variants should refuse a named one")
	}
	// A variant list none of which applies fails, naming the flag to pass
	only := selectRecipe()
	only.variants = only.variants[:1]
	if _, err := Select(only, &v1.HostProfile{Facts: map[string]string{}}, Options{}); err == nil {
		t.Fatal("no applicable variant should fail")
	}
}

func TestSelectOCI(t *testing.T) {
	rc := selectRecipe()
	sel, err := Select(rc, gpuProfile(), Options{Sandbox: v1.SandboxKind_SANDBOX_KIND_OCI, Image: "img:1", Defaults: &v1.Builds{Cli: []string{"sh"}}})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Variant.ID != "gpu" {
		t.Fatalf("tools are not checked for containers, got %s", sel.Variant.ID)
	}
	if sel.CLI == "" || sel.Image != "img:1" || sel.Err() != nil {
		t.Fatalf("oci selection cli=%q image=%q unmet=%v", sel.CLI, sel.Image, sel.Unmet)
	}
	sel, err = Select(rc, gpuProfile(), Options{Sandbox: v1.SandboxKind_SANDBOX_KIND_OCI, Defaults: &v1.Builds{Cli: []string{"no-such-cli-abc"}}})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Err() == nil || len(sel.Unmet) != 2 {
		t.Fatalf("missing cli and image should both be unmet: %v", sel.Unmet)
	}
	// The variant's image and the recipe's default sandbox stand in when the request names none
	rc.variants[0].Image = "variant:img"
	rc.sandbox = recipes.Sandbox{Kind: v1.SandboxKind_SANDBOX_KIND_OCI, CLIs: []string{"sh"}}
	sel, err = Select(rc, gpuProfile(), Options{})
	if err != nil || sel.Sandbox != v1.SandboxKind_SANDBOX_KIND_OCI || sel.Image != "variant:img" || sel.CLI == "" {
		t.Fatalf("recipe sandbox %v %v", sel, err)
	}
}

func TestRegistryRefusesBrokenRecipes(t *testing.T) {
	good := selectRecipe()
	bad := map[string]fakeRecipe{
		"no id":                   {runtimeID: "r", binary: "b"},
		"no runtime":              {id: "x", binary: "b"},
		"no binary":               {id: "x", runtimeID: "r"},
		"variant without applies": {id: "x", runtimeID: "r", binary: "b", variants: []recipes.Variant{{ID: "v"}}},
		"variant without id":      {id: "x", runtimeID: "r", binary: "b", variants: []recipes.Variant{{Applies: always}}},
	}
	for name, rc := range bad {
		if _, err := New([]recipes.Recipe{rc}); err == nil {
			t.Errorf("%s should fail", name)
		}
	}
	if _, err := New([]recipes.Recipe{good, good}); err == nil {
		t.Fatal("duplicate id should fail")
	}
}

func TestHashChangesWithInputs(t *testing.T) {
	rc := selectRecipe()
	base := func() *v1.Build {
		return &v1.Build{Variant: "cpu", Ref: "1.0", Sandbox: v1.SandboxKind_SANDBOX_KIND_HOST, Vars: map[string]string{"a": "1"}, Facts: map[string]string{"f": "x"}, Patches: []string{"p"}}
	}
	patches := map[string][]byte{"p": []byte("diff")}
	bctx := func(b *v1.Build) *recipes.Build {
		return &recipes.Build{Ref: b.GetRef(), Variant: b.GetVariant(), Vars: b.GetVars()}
	}
	id := hashBuild(rc, base(), bctx(base()), patches)
	if len(id) != idLength || id != hashBuild(rc, base(), bctx(base()), patches) {
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
		if hashBuild(rc, b, bctx(b), patches) == id {
			t.Errorf("%s change should change the hash", name)
		}
	}
	if hashBuild(rc, base(), bctx(base()), map[string][]byte{"p": []byte("other diff")}) == id {
		t.Error("patch content change should change the hash")
	}
	// The steps a build would run are part of its identity, so a recipe change rebuilds
	other := rc
	other.steps = func(b *recipes.Build) []recipes.Step {
		return []recipes.Step{{Name: "two", Command: []string{"sh", "-c", "false"}}}
	}
	if hashBuild(other, base(), bctx(base()), patches) == id {
		t.Error("step change should change the hash")
	}
	other = rc
	other.binary = "elsewhere"
	if hashBuild(other, base(), bctx(base()), patches) == id {
		t.Error("binary change should change the hash")
	}
}

func TestRegistryForRuntime(t *testing.T) {
	rc := selectRecipe()
	reg := registry(t, rc)
	if len(reg.ForRuntime("fake")) != 1 || len(reg.ForRuntime("other")) != 0 || len(reg.List()) != 1 {
		t.Fatal("registry lookups")
	}
	if _, err := reg.Get("missing"); err == nil {
		t.Fatal("missing recipe")
	}
	if got, err := reg.Get("fake"); err != nil || got.ID() != "fake" {
		t.Fatalf("get %v %v", got, err)
	}
	if Timeout(rc) != DefaultTimeout {
		t.Fatal("default timeout")
	}
	rc.timeout = 1
	if Timeout(rc) != 1 {
		t.Fatal("recipe timeout")
	}
	d := Describe(rc, gpuProfile())
	if d.GetId() != "fake" || d.GetRuntimeId() != "fake" || len(d.GetVariants()) != 2 || d.GetVariants()[0].GetTools()[0] != "definitely-missing-tool-xyz" || len(d.GetVars()) != 1 || d.GetVars()[0].GetName() != "extra" || d.GetTimeoutMs() != 0 {
		t.Fatalf("describe %v", d)
	}
	if got := Variants(rc, gpuProfile()); len(got) != 2 {
		t.Fatalf("both variants apply to a gpu host: %v", got)
	}
	if got := Variants(rc, &v1.HostProfile{}); len(got) != 1 || got[0].ID != "cpu" {
		t.Fatalf("only cpu applies without devices: %v", got)
	}
}
