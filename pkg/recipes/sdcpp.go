package recipes

import (
	"strconv"
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// sd-server compiled from the stable-diffusion.cpp source with the backend the host's devices take
type SDCpp struct{}

func (SDCpp) ID() string        { return "sdcpp" }
func (SDCpp) RuntimeID() string { return "sdcpp" }
func (SDCpp) Description() string {
	return "sd-server compiled from a tag, branch, or commit of stable-diffusion.cpp"
}

// The tree is cloned rather than fetched as an archive, since ggml and the image codecs come as submodules
func (SDCpp) Source() Source {
	return Source{Releases: "leejet/stable-diffusion.cpp", Repo: "https://github.com/leejet/stable-diffusion.cpp"}
}

// git checks the tree and its submodules out, cmake configures and drives the build, cc and c++ compile it
func (SDCpp) Tools() []string { return []string{"git", "cmake", "cc", "c++"} }
func (SDCpp) Facts() []string {
	return []string{"device.vendor", "device.compute_capability", "device.driver_version"}
}
func (SDCpp) Vars() []Var {
	return []Var{
		{Name: "build_type", Label: "Build type", Default: "Release", Description: "The cmake build type", Choices: []string{"Release", "RelWithDebInfo", "Debug", "MinSizeRel"}},
		{Name: "extra", Label: "Extra cmake flags", Description: "Further cmake flags for the configure step, separated by spaces, -DGGML_CUDA_F16=ON say"},
	}
}

func (SDCpp) Variants() []Variant {
	return []Variant{
		{
			ID:          "cuda",
			Description: "CUDA kernels for the probed compute capabilities",
			Tools:       []string{"nvcc"},
			Requires:    "an NVIDIA device",
			Applies:     func(h *v1.HostProfile) bool { return posix(h) && host.HasVendor(h, "nvidia") },
			Vars: func(h *v1.HostProfile) map[string]string {
				return map[string]string{"backend": "-DSD_CUDA=ON", "archs": "-DCMAKE_CUDA_ARCHITECTURES=" + cudaArchitectures(h)}
			},
		},
		{
			ID:          "rocm",
			Description: "HIP kernels for AMD devices",
			Tools:       []string{"hipcc"},
			Requires:    "an AMD device",
			Applies:     func(h *v1.HostProfile) bool { return posix(h) && host.HasVendor(h, "amd") },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DSD_HIPBLAS=ON"} },
		},
		{
			ID:          "metal",
			Description: "Metal on Apple silicon",
			Requires:    "Apple silicon",
			Applies:     func(h *v1.HostProfile) bool { return host.Is(h, "darwin", "arm64") },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DSD_METAL=ON"} },
		},
		{
			ID:          "vulkan",
			Description: "Vulkan shaders, works on any GPU with a Vulkan driver",
			Tools:       []string{"glslc"},
			Requires:    "a GPU",
			Applies:     func(h *v1.HostProfile) bool { return posix(h) && host.HasGPU(h) },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DSD_VULKAN=ON"} },
		},
		{
			ID:          "cpu",
			Description: "CPU only with native instruction selection",
			Applies:     posix,
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "-DGGML_NATIVE=ON"} },
		},
	}
}

func (SDCpp) Sandbox() Sandbox {
	return Sandbox{Kind: v1.SandboxKind_SANDBOX_KIND_HOST, CLIs: []string{"podman", "docker", "nerdctl"}}
}

// The submodules come first, then a static server without the bundled web page, which nebu's own console replaces
func (SDCpp) Steps(b *Build) []Step {
	configure := command("cmake", "-S", ".", "-B", "build",
		"-DCMAKE_BUILD_TYPE="+arg(b.Vars, "build_type"), arg(b.Vars, "backend"), arg(b.Vars, "archs"),
		"-DSD_BUILD_SHARED_LIBS=OFF", "-DSD_BUILD_EXAMPLES=ON", "-DSD_SERVER_BUILD_FRONTEND=OFF")
	configure = append(configure, args(b.Vars, "extra")...)
	return []Step{
		{Name: "submodules", Command: command("git", "submodule", "update", "--init", "--recursive", "--depth", "1")},
		{Name: "configure", Command: configure},
		{Name: "compile", Command: command("cmake", "--build", "build", "--config", arg(b.Vars, "build_type"), "--target", "sd-server", "-j", strconv.Itoa(b.Jobs))},
	}
}

func (SDCpp) Outputs() []string      { return []string{"build/bin/sd-server"} }
func (SDCpp) Binary() string         { return "sd-server" }
func (SDCpp) Timeout() time.Duration { return 3 * time.Hour }
func (SDCpp) Patches() []Patch       { return nil }
