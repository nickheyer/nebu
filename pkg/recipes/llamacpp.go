package recipes

import (
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// llama-server built from a release tag with the backend the host's devices take
type LlamaCpp struct{}

func (LlamaCpp) ID() string          { return "llamacpp" }
func (LlamaCpp) RuntimeID() string   { return "llamacpp" }
func (LlamaCpp) Description() string { return "llama-server built from a release tag" }

func (LlamaCpp) Source() Source {
	return Source{
		Releases: "ggml-org/llama.cpp",
		Archive: func(ref string) string {
			return "https://github.com/ggml-org/llama.cpp/archive/refs/tags/" + ref + ".tar.gz"
		},
	}
}

func (LlamaCpp) Tools() []string { return []string{"cmake"} }
func (LlamaCpp) Facts() []string {
	return []string{"device.vendor", "device.compute_capability", "nvidia.driver_version"}
}
func (LlamaCpp) Vars() map[string]string {
	return map[string]string{"build_type": "Release", "extra": ""}
}

func (LlamaCpp) Variants() []Variant {
	return []Variant{
		{
			ID:          "cuda",
			Description: "CUDA kernels for the probed compute capabilities",
			Tools:       []string{"nvcc"},
			Applies:     func(h *v1.HostProfile) bool { return host.HasVendor(h, "nvidia") },
			Vars: func(h *v1.HostProfile) map[string]string {
				return map[string]string{"backend": "-DGGML_CUDA=ON", "archs": "-DCMAKE_CUDA_ARCHITECTURES=" + cudaArchitectures(h)}
			},
		},
		{
			ID:          "rocm",
			Description: "HIP kernels for AMD devices",
			Tools:       []string{"hipcc"},
			Applies:     func(h *v1.HostProfile) bool { return host.HasVendor(h, "amd") },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DGGML_HIP=ON"} },
		},
		{
			ID:          "metal",
			Description: "Metal on Apple silicon",
			Applies:     func(h *v1.HostProfile) bool { return host.Is(h, "darwin", "arm64") },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DGGML_METAL=ON"} },
		},
		{
			ID:          "vulkan",
			Description: "Vulkan shaders, works on any GPU with a Vulkan driver",
			Tools:       []string{"glslc"},
			Applies:     host.HasGPU,
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DGGML_VULKAN=ON"} },
		},
		{
			ID:          "cpu",
			Description: "CPU only with native instruction selection",
			Applies:     func(*v1.HostProfile) bool { return true },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DGGML_NATIVE=ON"} },
		},
	}
}

// The compute capabilities of every NVIDIA device as CMake lists them, 8.6 becoming 86
func cudaArchitectures(h *v1.HostProfile) string {
	var out []string
	for _, d := range host.Vendor(h, "nvidia") {
		if cc := strings.ReplaceAll(d.GetFacts()["compute_capability"], ".", ""); cc != "" {
			out = append(out, cc)
		}
	}
	return strings.Join(out, ";")
}

func (LlamaCpp) Sandbox() Sandbox {
	return Sandbox{Kind: v1.SandboxKind_SANDBOX_KIND_HOST, CLIs: []string{"podman", "docker", "nerdctl"}}
}

func (LlamaCpp) Steps(b *Build) []Step {
	return []Step{
		{Name: "configure", Command: command("cmake", "-S", ".", "-B", "build",
			"-DCMAKE_BUILD_TYPE="+arg(b.Vars, "build_type"), arg(b.Vars, "backend"), arg(b.Vars, "archs"),
			"-DBUILD_SHARED_LIBS=OFF", "-DLLAMA_CURL=OFF", "-DLLAMA_BUILD_TESTS=OFF", "-DLLAMA_BUILD_EXAMPLES=OFF", "-DLLAMA_BUILD_SERVER=ON",
			arg(b.Vars, "extra"))},
		{Name: "compile", Command: command("cmake", "--build", "build", "--config", arg(b.Vars, "build_type"), "--target", "llama-server", "-j", strconv.Itoa(b.Jobs))},
	}
}

func (LlamaCpp) Outputs() []string      { return []string{"build/bin/llama-server"} }
func (LlamaCpp) Binary() string         { return "llama-server" }
func (LlamaCpp) Timeout() time.Duration { return 3 * time.Hour }
func (LlamaCpp) Patches() []Patch       { return nil }
