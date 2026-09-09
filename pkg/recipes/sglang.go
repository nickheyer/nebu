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
func (SGLang) Vars() map[string]string {
	return map[string]string{"version": ""}
}

func (SGLang) Variants() []Variant {
	return []Variant{
		{
			ID:          "cuda",
			Description: "The PyPI wheels, built for CUDA",
			Applies:     func(h *v1.HostProfile) bool { return host.HasVendor(h, "nvidia") },
			Vars:        func(*v1.HostProfile) map[string]string { return nil },
		},
		{
			ID:          "cpu",
			Description: "The same wheels over CPU torch, so the recipe builds anywhere; serving still needs the NVIDIA GPU the runtime asks for",
			Applies:     func(*v1.HostProfile) bool { return true },
			Vars: func(*v1.HostProfile) map[string]string {
				return map[string]string{"index": "--extra-index-url=https://download.pytorch.org/whl/cpu"}
			},
		},
	}
}

func (SGLang) Sandbox() Sandbox {
	return Sandbox{Kind: v1.SandboxKind_SANDBOX_KIND_HOST, CLIs: []string{"podman", "docker", "nerdctl"}}
}

func (SGLang) Steps(b *Build) []Step {
	python := b.Out + "/venv/bin/python"
	return []Step{
		{Name: "venv", Command: command("python3", "-m", "venv", b.Out+"/venv")},
		{Name: "pip", Command: command(python, "-m", "pip", "install", "--upgrade", "pip")},
		{Name: "install", Command: command(python, "-m", "pip", "install", pin("sglang[all]", arg(b.Vars, "version")), arg(b.Vars, "index"))},
	}
}

func (SGLang) Outputs() []string      { return nil }
func (SGLang) Binary() string         { return "venv/bin/python" }
func (SGLang) Timeout() time.Duration { return time.Hour }
func (SGLang) Patches() []Patch       { return nil }
