package recipes

import (
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// SGLang installed from PyPI into a virtual environment
type SGLang struct{}

func (SGLang) ID() string          { return "sglang" }
func (SGLang) RuntimeID() string   { return "sglang" }
func (SGLang) Description() string { return "SGLang installed from PyPI into a virtual environment" }
func (SGLang) Source() Source      { return Source{} }
func (SGLang) Tools() []string     { return []string{"python3"} }
func (SGLang) Facts() []string     { return []string{"device.vendor", "device.compute_capability"} }
func (SGLang) Vars() []Var {
	return []Var{{Name: "version", Label: "Version", Description: "PyPI version, latest when empty"}}
}

// The wheels are built for CUDA 13 and need Python 3.11 or newer
func (SGLang) Variants() []Variant {
	return []Variant{
		{
			ID:          "cuda",
			Description: "The PyPI wheels, built for CUDA 13",
			Requires:    "an NVIDIA device",
			Applies:     func(h *v1.HostProfile) bool { return host.HasVendor(h, "nvidia") },
			Vars:        func(*v1.HostProfile) map[string]string { return nil },
		},
	}
}

func (SGLang) Sandbox() Sandbox {
	return Sandbox{Kind: v1.SandboxKind_SANDBOX_KIND_HOST, CLIs: []string{"podman", "docker", "nerdctl"}}
}

// uv installs sglang as its guide says, with pre-releases allowed for the dependencies that
// publish none other
func (SGLang) Steps(b *Build) []Step {
	python := b.Out + "/venv/bin/python"
	return []Step{
		{Name: "venv", Command: command("python3", "-m", "venv", b.Out+"/venv")},
		{Name: "pip", Command: command(python, "-m", "pip", "install", "--upgrade", "pip", "uv")},
		{Name: "install", Command: command(python, "-m", "uv", "pip", "install", "--python", python, "--prerelease=allow", pin("sglang", arg(b.Vars, "version")))},
		{Name: "nixl", Command: command(python, "-m", "uv", "pip", "install", "--python", python, "nixl; platform_system == 'Linux'")},
	}
}

func (SGLang) Outputs() []string      { return nil }
func (SGLang) Binary() string         { return "venv/bin/python" }
func (SGLang) Timeout() time.Duration { return time.Hour }
func (SGLang) Patches() []Patch       { return nil }
