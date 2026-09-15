package build

import (
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/recipes"
)

// A recipe assembled field by field, so a test can shape one without a runtime behind it
type fakeRecipe struct {
	id, runtimeID string
	source        recipes.Source
	tools, facts  []string
	vars          []recipes.Var
	variants      []recipes.Variant
	sandbox       recipes.Sandbox
	steps         func(b *recipes.Build) []recipes.Step
	outputs       []string
	binary        string
	timeout       time.Duration
	patches       []recipes.Patch
}

func (r fakeRecipe) ID() string                  { return r.id }
func (r fakeRecipe) RuntimeID() string           { return r.runtimeID }
func (r fakeRecipe) Description() string         { return "a recipe for tests" }
func (r fakeRecipe) Source() recipes.Source      { return r.source }
func (r fakeRecipe) Tools() []string             { return r.tools }
func (r fakeRecipe) Facts() []string             { return r.facts }
func (r fakeRecipe) Vars() []recipes.Var         { return r.vars }
func (r fakeRecipe) Variants() []recipes.Variant { return r.variants }
func (r fakeRecipe) Sandbox() recipes.Sandbox    { return r.sandbox }
func (r fakeRecipe) Outputs() []string           { return r.outputs }
func (r fakeRecipe) Binary() string              { return r.binary }
func (r fakeRecipe) Timeout() time.Duration      { return r.timeout }
func (r fakeRecipe) Patches() []recipes.Patch    { return r.patches }

func (r fakeRecipe) Steps(b *recipes.Build) []recipes.Step {
	if r.steps == nil {
		return nil
	}
	return r.steps(b)
}

func always(*v1.HostProfile) bool { return true }

// Whether any device of the profile is a GPU
func anyGPU(h *v1.HostProfile) bool {
	for _, d := range h.GetDevices() {
		if d.GetKind() == v1.DeviceKind_DEVICE_KIND_GPU {
			return true
		}
	}
	return false
}

// A command with its empty arguments dropped, the way a recipe leaves an unset flag out
func args(parts ...string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// The recipe the selection tests share: a release fed archive, a gpu variant needing a tool no host has, and a cpu fallback
func selectRecipe() fakeRecipe {
	return fakeRecipe{
		id: "fake", runtimeID: "fake",
		source: recipes.Source{Releases: "o/r", Archive: func(ref string) string { return "http://example/fake-" + ref + ".tar.gz" }},
		tools:  []string{"sh"},
		facts:  []string{"cpu.count", "device.vendor", "device.compute_capability"},
		vars:   []recipes.Var{{Name: "extra"}},
		variants: []recipes.Variant{
			{ID: "gpu", Tools: []string{"definitely-missing-tool-xyz"}, Applies: anyGPU, Vars: func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "gpu"} }},
			{ID: "cpu", Applies: always, Vars: func(*v1.HostProfile) map[string]string { return map[string]string{"backend": "cpu"} }},
		},
		steps: func(b *recipes.Build) []recipes.Step {
			return []recipes.Step{{Name: "one", Command: []string{"sh", "-c", "true"}}}
		},
		outputs: []string{"bin"},
		binary:  "bin",
	}
}

func registry(t interface{ Fatal(...any) }, rcs ...recipes.Recipe) *Registry {
	reg, err := New(rcs)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
