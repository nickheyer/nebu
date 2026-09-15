package recipes

import (
	"slices"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func nvidiaLinux() *v1.HostProfile {
	return &v1.HostProfile{Os: "linux", Arch: "amd64", Devices: []*v1.Device{{Id: "gpu0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "nvidia", Facts: map[string]string{"compute_capability": "8.6"}}}}
}

func variant(t *testing.T, id string) Variant {
	t.Helper()
	for _, v := range (LlamaCpp{}).Variants() {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("no variant %s", id)
	return Variant{}
}

func TestLlamaCppArchiveTakesAnyRef(t *testing.T) {
	src := LlamaCpp{}.Source()
	for _, ref := range []string{"b7000", "master", "0123abcd"} {
		if got, want := src.Archive(ref), "https://github.com/ggml-org/llama.cpp/archive/"+ref+".tar.gz"; got != want {
			t.Fatalf("archive of %s: %s", ref, got)
		}
	}
	if !src.Fetched() || src.String() != "github.com/ggml-org/llama.cpp" {
		t.Fatalf("source %+v", src)
	}
}

func TestLlamaCppExtraFlagsAreSeparateArguments(t *testing.T) {
	steps := LlamaCpp{}.Steps(&Build{Vars: map[string]string{"build_type": "Release", "backend": "-DGGML_CUDA=ON", "archs": "-DCMAKE_CUDA_ARCHITECTURES=86", "extra": " -DGGML_CUDA_F16=ON  -DGGML_LTO=ON "}, Jobs: 4})
	configure := steps[0].Command
	for _, flag := range []string{"-DGGML_CUDA=ON", "-DCMAKE_CUDA_ARCHITECTURES=86", "-DGGML_CUDA_F16=ON", "-DGGML_LTO=ON"} {
		if !slices.Contains(configure, flag) {
			t.Fatalf("configure lacks %s as its own argument: %v", flag, configure)
		}
	}
	bare := LlamaCpp{}.Steps(&Build{Vars: map[string]string{"build_type": "Release"}, Jobs: 1})[0].Command
	if slices.Contains(bare, "") {
		t.Fatalf("an unset variable must not land as an empty argument: %v", bare)
	}
}

func TestLlamaCppVarsAreDescribed(t *testing.T) {
	for _, v := range (LlamaCpp{}).Vars() {
		if v.Label == "" || v.Description == "" {
			t.Fatalf("var %s needs a label and a description: %+v", v.Name, v)
		}
	}
	build := LlamaCpp{}.Vars()[0]
	if build.Name != "build_type" || build.Default != "Release" || !slices.Contains(build.Choices, "Debug") {
		t.Fatalf("build_type %+v", build)
	}
}

func TestLlamaCppVariantsSayWhatTheyRequire(t *testing.T) {
	for _, v := range (LlamaCpp{}).Variants() {
		if v.ID != "cpu" && v.Requires == "" {
			t.Fatalf("variant %s needs a requires phrase", v.ID)
		}
	}
	linux := nvidiaLinux()
	if !variant(t, "cuda").Applies(linux) || !variant(t, "cpu").Applies(linux) || variant(t, "rocm").Applies(linux) {
		t.Fatal("cuda and cpu apply to an NVIDIA linux host, rocm does not")
	}
	if vars := variant(t, "cuda").Vars(linux); vars["archs"] != "-DCMAKE_CUDA_ARCHITECTURES=86" {
		t.Fatalf("cuda vars %v", vars)
	}
	windows := nvidiaLinux()
	windows.Os = "windows"
	for _, v := range (LlamaCpp{}).Variants() {
		if v.Applies(windows) {
			t.Fatalf("variant %s must not apply on windows, where the outputs are not built", v.ID)
		}
	}
}
