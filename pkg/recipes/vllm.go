package recipes

import (
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// vllm installed from its wheel index into a virtual environment
type VLLM struct{}

func (VLLM) ID() string        { return "vllm" }
func (VLLM) RuntimeID() string { return "vllm" }
func (VLLM) Description() string {
	return "vllm installed from its wheel index into a virtual environment"
}
func (VLLM) Source() Source  { return Source{} }
func (VLLM) Tools() []string { return nil }
func (VLLM) Facts() []string { return []string{"device.vendor", "device.compute_capability"} }
func (VLLM) Vars() []Var {
	return []Var{{Name: "version", Label: "Version", Description: "vllm version, latest when empty"}}
}

// The CUDA wheels come from PyPI with the torch build uv picks for the installed driver. The
// ROCm wheels come from vLLM's own index, are built for Python 3.12 alone, and bundle their torch
func (VLLM) Variants() []Variant {
	return []Variant{
		{
			ID:          "cuda",
			Description: "The PyPI wheel, built for CUDA, with the torch build for this driver",
			Tools:       []string{"python3"},
			Requires:    "an NVIDIA device",
			Applies:     func(h *v1.HostProfile) bool { return host.HasVendor(h, "nvidia") },
			Vars: func(*v1.HostProfile) map[string]string {
				return map[string]string{"python": "python3", "backend": "--torch-backend=auto"}
			},
		},
		{
			ID:          "rocm",
			Description: "ROCm wheels from vLLM's wheel index, Python 3.12",
			Tools:       []string{"python3.12"},
			Requires:    "an AMD device",
			Applies:     func(h *v1.HostProfile) bool { return host.HasVendor(h, "amd") },
			Vars: func(*v1.HostProfile) map[string]string {
				return map[string]string{"python": "python3.12", "index": "--extra-index-url=https://wheels.vllm.ai/rocm/"}
			},
		},
	}
}

func (VLLM) Sandbox() Sandbox {
	return Sandbox{Kind: v1.SandboxKind_SANDBOX_KIND_HOST, CLIs: []string{"podman", "docker", "nerdctl"}}
}

// uv resolves the wheel, since it ranks an extra index above PyPI and picks the torch build
func (VLLM) Steps(b *Build) []Step {
	python := b.Out + "/venv/bin/python"
	return []Step{
		{Name: "venv", Command: command(arg(b.Vars, "python"), "-m", "venv", b.Out+"/venv")},
		{Name: "pip", Command: command(python, "-m", "pip", "install", "--upgrade", "pip", "uv")},
		{Name: "install", Command: command(python, "-m", "uv", "pip", "install", "--python", python, pin("vllm", arg(b.Vars, "version")), arg(b.Vars, "backend"), arg(b.Vars, "index"))},
		{Name: "nixl", Command: command(python, "-m", "uv", "pip", "install", "--python", python, "nixl; platform_system == 'Linux'")},
	}
}

func (VLLM) Outputs() []string      { return nil }
func (VLLM) Binary() string         { return "venv/bin/vllm" }
func (VLLM) Timeout() time.Duration { return time.Hour }
func (VLLM) Patches() []Patch       { return nil }

// A PyPI requirement pinned to a version when one is set
func pin(pkg, version string) string {
	if version == "" {
		return pkg
	}
	return pkg + "==" + version
}
