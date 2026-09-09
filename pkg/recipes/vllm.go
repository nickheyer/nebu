package recipes

import (
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// vllm installed from PyPI into a virtual environment
type VLLM struct{}

func (VLLM) ID() string          { return "vllm" }
func (VLLM) RuntimeID() string   { return "vllm" }
func (VLLM) Description() string { return "vllm installed from PyPI into a virtual environment" }
func (VLLM) Source() Source      { return Source{} }
func (VLLM) Tools() []string     { return []string{"python3"} }
func (VLLM) Facts() []string     { return []string{"device.vendor", "device.compute_capability"} }
func (VLLM) Vars() map[string]string {
	return map[string]string{"version": ""}
}

func (VLLM) Variants() []Variant {
	return []Variant{
		{
			ID:          "cuda",
			Description: "The default wheel, built for CUDA",
			Applies:     func(h *v1.HostProfile) bool { return host.HasVendor(h, "nvidia") },
			Vars:        func(*v1.HostProfile) map[string]string { return nil },
		},
		{
			ID:          "rocm",
			Description: "ROCm wheels from the AMD index",
			Applies:     func(h *v1.HostProfile) bool { return host.HasVendor(h, "amd") },
			Vars: func(*v1.HostProfile) map[string]string {
				return map[string]string{"index": "--extra-index-url=https://download.pytorch.org/whl/rocm6.3"}
			},
		},
		{
			ID:          "cpu",
			Description: "CPU only wheel",
			Applies:     func(*v1.HostProfile) bool { return true },
			Vars: func(*v1.HostProfile) map[string]string {
				return map[string]string{"index": "--extra-index-url=https://download.pytorch.org/whl/cpu"}
			},
		},
	}
}

func (VLLM) Sandbox() Sandbox {
	return Sandbox{Kind: v1.SandboxKind_SANDBOX_KIND_HOST, CLIs: []string{"podman", "docker", "nerdctl"}}
}

func (VLLM) Steps(b *Build) []Step {
	python := b.Out + "/venv/bin/python"
	return []Step{
		{Name: "venv", Command: command("python3", "-m", "venv", b.Out+"/venv")},
		{Name: "pip", Command: command(python, "-m", "pip", "install", "--upgrade", "pip")},
		{Name: "install", Command: command(python, "-m", "pip", "install", pin("vllm", arg(b.Vars, "version")), arg(b.Vars, "index"))},
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
