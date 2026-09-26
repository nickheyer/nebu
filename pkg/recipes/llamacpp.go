package recipes

import (
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Builds llama-server with the host backend.
type LlamaCpp struct{}

func (LlamaCpp) ID() string        { return "llamacpp" }
func (LlamaCpp) RuntimeID() string { return "llamacpp" }
func (LlamaCpp) Description() string {
	return "llama-server compiled from a tag, branch, or commit of llama.cpp"
}

func (LlamaCpp) Source() Source {
	return Source{
		Releases: "ggml-org/llama.cpp",
		// GitHub serves a tag, a branch, or a commit alike at this path
		Archive: func(ref string) string {
			return "https://github.com/ggml-org/llama.cpp/archive/" + ref + ".tar.gz"
		},
	}
}

// cmake configures and drives the build, cc and c++ compile it
func (LlamaCpp) Tools() []string { return []string{"cmake", "cc", "c++"} }
func (LlamaCpp) Facts() []string {
	return []string{"device.vendor", "device.compute_capability", "device.driver_version"}
}
func (LlamaCpp) Vars() []Var {
	return []Var{
		{Name: "build_type", Label: "Build type", Default: "Release", Description: "The cmake build type", Choices: []string{"Release", "RelWithDebInfo", "Debug", "MinSizeRel"}},
		{Name: "extra", Label: "Extra cmake flags", Description: "Space-separated CMake flags, such as -DGGML_CUDA_F16=ON"},
	}
}

// Builds on Linux and macOS. Windows uses published builds.
func posix(h *v1.HostProfile) bool { return h.GetOs() != "windows" }

func (LlamaCpp) Variants() []Variant {
	return []Variant{
		{
			ID:          "cuda",
			Description: "CUDA kernels for the probed compute capabilities",
			Tools:       []string{"nvcc"},
			Requires:    "an NVIDIA device",
			Applies:     func(h *v1.HostProfile) bool { return posix(h) && host.HasVendor(h, "nvidia") },
			Vars: func(h *v1.HostProfile) map[string]string {
				return map[string]string{"backend": "-DGGML_CUDA=ON", "archs": "-DCMAKE_CUDA_ARCHITECTURES=" + cudaArchitectures(h)}
			},
		},
		{
			ID:          "rocm",
			Description: "HIP kernels for AMD devices",
			Tools:       []string{"hipcc"},
			Requires:    "an AMD device",
			Applies:     func(h *v1.HostProfile) bool { return posix(h) && host.HasVendor(h, "amd") },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DGGML_HIP=ON"} },
		},
		{
			ID:          "metal",
			Description: "Metal on Apple silicon",
			Requires:    "Apple silicon",
			Applies:     func(h *v1.HostProfile) bool { return host.Is(h, "darwin", "arm64") },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DGGML_METAL=ON"} },
		},
		{
			ID:          "vulkan",
			Description: "Vulkan shaders, works on any GPU with a Vulkan driver",
			Tools:       []string{"glslc"},
			Requires:    "a GPU",
			Applies:     func(h *v1.HostProfile) bool { return posix(h) && host.HasGPU(h) },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DGGML_VULKAN=ON"} },
		},
		{
			ID:          "cpu",
			Description: "CPU only with native instruction selection",
			Applies:     posix,
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DGGML_NATIVE=ON"} },
		},
	}
}

// Formats NVIDIA compute capabilities for CMake, such as 8.6 to 86.
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
	configure := command("cmake", "-S", ".", "-B", "build",
		"-DCMAKE_BUILD_TYPE="+arg(b.Vars, "build_type"), arg(b.Vars, "backend"), arg(b.Vars, "archs"),
		"-DBUILD_SHARED_LIBS=OFF", "-DLLAMA_CURL=OFF", "-DLLAMA_BUILD_TESTS=OFF", "-DLLAMA_BUILD_EXAMPLES=OFF", "-DLLAMA_BUILD_SERVER=ON", "-DLLAMA_BUILD_TOOLS=ON", "-DGGML_RPC=ON")
	configure = append(configure, args(b.Vars, "extra")...)
	return []Step{
		{Name: "configure", Command: configure},
		{Name: "compile", Command: command("cmake", "--build", "build", "--config", arg(b.Vars, "build_type"), "-j", strconv.Itoa(b.Jobs))},
	}
}

// The server and the rpc server under whatever name this revision builds it
func (LlamaCpp) Outputs() []string {
	return []string{"build/bin/llama-server", "build/bin/*rpc-server*"}
}
func (LlamaCpp) Binary() string         { return "llama-server" }
func (LlamaCpp) Timeout() time.Duration { return 3 * time.Hour }
func (LlamaCpp) Patches() []Patch       { return nil }
