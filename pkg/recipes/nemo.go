package recipes

import (
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// NeMo Export-Deploy from PyPI in a virtual environment, with NeMo's checkpoint converter in a second one
type NeMo struct{}

func (NeMo) ID() string        { return "nemo" }
func (NeMo) RuntimeID() string { return "nemo" }
func (NeMo) Description() string {
	return "NeMo Export-Deploy from PyPI in a virtual environment, with NeMo's checkpoint converter in a second one"
}

func (NeMo) Source() Source {
	return Source{
		Releases: "NVIDIA-NeMo/Export-Deploy",
		// GitHub serves a tag, a branch, or a commit alike at this path
		Archive: func(ref string) string {
			return "https://github.com/NVIDIA-NeMo/Export-Deploy/archive/" + ref + ".tar.gz"
		},
	}
}

func (NeMo) Tools() []string { return []string{"curl"} }
func (NeMo) Facts() []string { return []string{"device.vendor", "device.compute_capability"} }

func (NeMo) Vars() []Var {
	return []Var{
		{Name: "version", Label: "Version", Description: "PyPI version, release tag when empty"},
		{Name: "converter", Label: "Converter version", Default: "2.5.3", Description: "nemo_toolkit converter version"},
	}
}

func (NeMo) Variants() []Variant {
	return []Variant{
		{
			ID:          "py312",
			Description: "Python 3.12 for Export-Deploy wheels",
			Tools:       []string{"python3.12"},
			Applies:     func(*v1.HostProfile) bool { return true },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"python": "python3.12"} },
		},
		{
			ID:          "py3",
			Description: "The host's python3",
			Tools:       []string{"python3"},
			Applies:     func(*v1.HostProfile) bool { return true },
			Vars:        func(*v1.HostProfile) map[string]string { return map[string]string{"python": "python3"} },
		},
	}
}

func (NeMo) Sandbox() Sandbox {
	return Sandbox{Kind: v1.SandboxKind_SANDBOX_KIND_HOST, CLIs: []string{"podman", "docker", "nerdctl"}}
}

func (NeMo) Steps(b *Build) []Step {
	python := arg(b.Vars, "python")
	version := arg(b.Vars, "version")
	if version == "" {
		version = strings.TrimPrefix(b.Ref, "v")
	}
	converter := arg(b.Vars, "converter")
	venv := b.Out + "/venv/bin/python"
	convert := b.Out + "/venv-convert/bin/python"
	return []Step{
		{Name: "venv", Command: command(python, "-m", "venv", b.Out+"/venv")},
		{Name: "pip", Command: command(venv, "-m", "pip", "install", "--upgrade", "pip")},
		{Name: "install", Command: command(venv, "-m", "pip", "install", "nemo-export-deploy=="+version)},
		{Name: "script", Command: command("cp", "scripts/deploy/nlp/deploy_ray_inframework.py", b.Out+"/deploy_ray_inframework.py")},
		{Name: "venv-convert", Command: command(python, "-m", "venv", b.Out+"/venv-convert")},
		{Name: "install-converter", Command: command(convert, "-m", "pip", "install", "nemo_toolkit[nlp]=="+converter)},
		{Name: "converter", Command: command("curl", "-fsSL", "https://raw.githubusercontent.com/NVIDIA/NeMo/v"+converter+"/scripts/checkpoint_converters/convert_nemo1_to_nemo2.py", "-o", b.Out+"/convert_nemo1_to_nemo2.py")},
	}
}

func (NeMo) Outputs() []string      { return nil }
func (NeMo) Binary() string         { return "deploy_ray_inframework.py" }
func (NeMo) Timeout() time.Duration { return 3 * time.Hour }
func (NeMo) Patches() []Patch       { return nil }
