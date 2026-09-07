package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/spec"
)

func registry(t *testing.T) *Registry {
	t.Helper()
	c, err := spec.Load(os.DirFS(filepath.Join("..", "..", "spec")))
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(c.Runtimes)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParams(t *testing.T) {
	rt, err := registry(t).Get("llamacpp")
	if err != nil {
		t.Fatal(err)
	}
	if !rt.Accepts("gguf") || rt.Accepts("safetensors") || rt.Policy == nil {
		t.Fatal("manifest basics")
	}
	p, err := rt.Params(map[string]string{"n_ctx": "4096", "cache_type_k": "q8_0"})
	if err != nil {
		t.Fatal(err)
	}
	if p["n_ctx"] != int64(4096) || p["n_gpu_layers"] != Auto || p["cache_type_k"] != "q8_0" || p["threads"] != int64(-1) {
		t.Fatalf("params %v", p)
	}
	if _, err := rt.Params(map[string]string{"bogus": "1"}); !errors.Is(err, ErrParam) {
		t.Fatalf("unknown param: %v", err)
	}
	if _, err := rt.Params(map[string]string{"cache_type_k": "q3_k"}); !errors.Is(err, ErrParam) {
		t.Fatalf("bad choice: %v", err)
	}
	if _, err := rt.Params(map[string]string{"n_ctx": "lots"}); !errors.Is(err, ErrParam) {
		t.Fatalf("bad int: %v", err)
	}
	p, err = rt.Params(map[string]string{"n_gpu_layers": "12"})
	if err != nil || p["n_gpu_layers"] != int64(12) {
		t.Fatalf("solved override %v %v", p, err)
	}
	if _, err := registry(t).Get("nope"); !errors.Is(err, ErrUnknownRuntime) {
		t.Fatal("unknown runtime")
	}
}

func TestCompatible(t *testing.T) {
	rt, _ := registry(t).Get("llamacpp")
	ok, unmet := rt.Compatible(&v1.HostProfile{})
	if ok || len(unmet) != 1 {
		t.Fatalf("empty host should be incompatible: %v", unmet)
	}
	ok, _ = rt.Compatible(&v1.HostProfile{Devices: []*v1.Device{{Kind: v1.DeviceKind_DEVICE_KIND_CPU}}})
	if !ok {
		t.Fatal("cpu should satisfy the device constraint")
	}
	vllm, _ := registry(t).Get("vllm")
	if ok, _ := vllm.Compatible(&v1.HostProfile{Devices: []*v1.Device{{Kind: v1.DeviceKind_DEVICE_KIND_CPU}}}); ok {
		t.Fatal("cpu only should not satisfy gpu constraint")
	}
	if ok, _ := vllm.Compatible(&v1.HostProfile{Devices: []*v1.Device{{Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "apple"}}}); ok {
		t.Fatal("a gpu without cuda or rocm should not satisfy the vendor constraint")
	}
	st := vllm.Status(&v1.HostProfile{Devices: []*v1.Device{{Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "nvidia"}}})
	if !st.GetCompatible() || st.GetManifest().GetId() != "vllm" {
		t.Fatalf("status %+v", st)
	}
}

func TestBadManifest(t *testing.T) {
	_, err := New([]*v1.RuntimeManifest{{Id: "x", Constraints: []*v1.Constraint{{Expr: "1 +"}}}})
	if err == nil {
		t.Fatal("bad constraint should fail")
	}
	_, err = New([]*v1.RuntimeManifest{{Id: "x", Params: []*v1.Param{{Name: "n", Type: v1.ParamType_PARAM_TYPE_INT, Default: "abc"}}}})
	if err == nil {
		t.Fatal("bad default should fail")
	}
}

func TestShippedPrebuiltRulesPickByHost(t *testing.T) {
	catalog, err := spec.Load(os.DirFS(filepath.Join("..", "..", "spec")))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := New(catalog.Runtimes)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := reg.Get("llamacpp")
	if err != nil {
		t.Fatal(err)
	}
	var release InstallMethod
	for _, m := range rt.Installs() {
		if m.Spec.GetPrebuilt() != nil {
			release = m
		}
	}
	if release.Spec == nil {
		t.Fatal("llamacpp should offer a prebuilt release")
	}
	gpu := func(vendor string) *v1.Device {
		return &v1.Device{Id: "g0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: vendor}
	}
	for _, tc := range []struct {
		profile *v1.HostProfile
		want    string
	}{
		{&v1.HostProfile{Os: "linux", Arch: "amd64", Devices: []*v1.Device{gpu("nvidia")}, Facts: map[string]string{"nvidia.driver_version": "581.0"}}, "vulkan-x64"},
		{&v1.HostProfile{Os: "linux", Arch: "amd64", Devices: []*v1.Device{gpu("amd")}}, "rocm"},
		{&v1.HostProfile{Os: "linux", Arch: "amd64"}, "ubuntu-x64"},
		{&v1.HostProfile{Os: "windows", Arch: "amd64", Devices: []*v1.Device{gpu("nvidia")}, Facts: map[string]string{"nvidia.driver_version": "581.15"}}, "cuda-13"},
		{&v1.HostProfile{Os: "windows", Arch: "amd64", Devices: []*v1.Device{gpu("nvidia")}, Facts: map[string]string{"nvidia.driver_version": "552.1"}}, "cuda-12"},
		{&v1.HostProfile{Os: "windows", Arch: "amd64", Devices: []*v1.Device{gpu("nvidia")}}, "cuda-12"},
		{&v1.HostProfile{Os: "windows", Arch: "amd64", Devices: []*v1.Device{gpu("intel")}}, "vulkan"},
		{&v1.HostProfile{Os: "darwin", Arch: "arm64"}, "macos-arm64"},
	} {
		rule, err := release.HostRule(tc.profile)
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.profile.GetOs(), tc.profile.GetArch(), err)
		}
		if rule == nil || !strings.Contains(rule.GetAssets()[0], tc.want) {
			t.Fatalf("%s/%s %v: picked %v, want %s", tc.profile.GetOs(), tc.profile.GetArch(), tc.profile.GetDevices(), rule.GetAssets(), tc.want)
		}
		if len(rule.GetAssets()) > 1 && !strings.Contains(rule.GetAssets()[1], "cudart") {
			t.Fatalf("cuda rule should carry the cudart companion: %v", rule.GetAssets())
		}
		if named, err := release.Rule(rule.GetId()); err != nil || named != rule {
			t.Fatalf("rule by id %v %v", named, err)
		}
	}
	if rule, _ := release.HostRule(&v1.HostProfile{Os: "plan9", Arch: "mips"}); rule != nil {
		t.Fatalf("no rule should hold on plan9, got %v", rule)
	}
	if rules, _ := release.Rules(&v1.HostProfile{Os: "linux", Arch: "amd64", Devices: []*v1.Device{gpu("nvidia")}}); len(rules) != 2 || rules[0].GetId() != "linux-vulkan" || rules[1].GetId() != "linux-cpu" {
		t.Fatalf("linux nvidia should be offered vulkan then cpu, got %v", rules)
	}
	if _, err := release.Rule("nope"); err == nil {
		t.Fatal("unknown rule should fail")
	}
	if _, err := rt.Install("nope"); err == nil {
		t.Fatal("unknown method should fail")
	}
	if _, err := New([]*v1.RuntimeManifest{{Id: "x", Acquire: &v1.Acquire{Methods: []*v1.InstallMethod{{Id: "a"}}}}}); err == nil {
		t.Fatal("a method without a how should fail")
	}
	adopt := &v1.InstallMethod{Id: "a", How: &v1.InstallMethod_Adopt{Adopt: &v1.Adopt{Names: []string{"b"}}}}
	if _, err := New([]*v1.RuntimeManifest{{Id: "x", Acquire: &v1.Acquire{Methods: []*v1.InstallMethod{adopt, adopt}}}}); err == nil {
		t.Fatal("duplicate ids should fail")
	}
	if _, err := New([]*v1.RuntimeManifest{{Id: "x", Acquire: &v1.Acquire{Methods: []*v1.InstallMethod{{Id: "r", How: &v1.InstallMethod_Prebuilt{Prebuilt: &v1.Prebuilt{Releases: "o/r", Rules: []*v1.PrebuiltRule{{Id: "b", Assets: []string{"("}, Binary: "x"}}}}}}}}}); err == nil {
		t.Fatal("a bad asset pattern should fail")
	}
}
