// Package recipes defines runtime build recipes.
package recipes

import (
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Source id releases are read from
const ReleaseSource = "github"

// Where a recipe's source tree comes from
type Source struct {
	// GitHub repository used to resolve latest releases.
	Releases string
	// Archive URL for a source ref.
	Archive func(ref string) string
	// Git repository cloned at the ref.
	Repo string
	// A directory inside the archive holding the tree
	Subdir string
}

// Source description.
func (s Source) String() string {
	switch {
	case s.Releases != "":
		return "github.com/" + s.Releases
	case s.Repo != "":
		return s.Repo
	}
	return "none"
}

// Reports whether the recipe fetches source code.
func (s Source) Fetched() bool {
	return s.Releases != "" || s.Repo != "" || s.Archive != nil
}

// User-configurable build variable.
type Var struct {
	Name string
	// Display label.
	Label       string
	Default     string
	Description string
	// Allowed values. Empty accepts any value.
	Choices []string
}

// Build variant selected for host capabilities.
type Variant struct {
	ID          string
	Description string
	// Tools the variant needs on the host beyond the recipe's own
	Tools []string
	// Host requirements. Empty means unrestricted.
	Requires string
	// Checks host support.
	Applies func(h *v1.HostProfile) bool
	// Variables the variant sets for this host
	Vars func(h *v1.HostProfile) map[string]string
	// OCI build image.
	Image string
}

// How steps are isolated from the host
type Sandbox struct {
	Kind v1.SandboxKind
	// The image an oci sandbox runs when config names none
	Image string
	// Container CLIs tried in order
	CLIs []string
	// Extra arguments the container CLI takes
	Args []string
}

// Resolved build inputs.
type Build struct {
	Ref     string
	Commit  string
	Variant string
	Vars    map[string]string
	// Sandbox paths for the build root, source, and outputs.
	Root, Src, Out string
	// Available parallel jobs.
	Jobs int
	Host *v1.HostProfile
}

// One command run inside the sandbox
type Step struct {
	Name    string
	Command []string
	Env     map[string]string
	// Working directory relative to source. Empty uses the source root.
	Dir string
}

// One unified diff applied to the tree
type Patch struct {
	ID string
	// Patch condition. Nil applies to all builds.
	Applies func(b *Build) bool
	// The diff itself, or where to fetch it
	Content []byte
	URL     string
	// Leading path components to strip, one when zero
	Strip int
}

// Runtime build recipe.
type Recipe interface {
	ID() string
	RuntimeID() string
	Description() string
	Source() Source
	// Tools every variant needs on the host
	Tools() []string
	// Host and device facts included in the build hash, such as device.vendor.
	Facts() []string
	// Build variables and defaults in display order.
	Vars() []Var
	Variants() []Variant
	Sandbox() Sandbox
	// Ordered build steps.
	Steps(b *Build) []Step
	// Paths under the source tree copied out after the steps, globs allowed
	Outputs() []string
	// Installed binary path relative to the output directory.
	Binary() string
	Timeout() time.Duration
	Patches() []Patch
}

// Every recipe nebu ships
func All() []Recipe {
	return []Recipe{LlamaCpp{}, VLLM{}, SGLang{}, NeMo{}, SDCpp{}}
}

// Expands a variable, omitting empty arguments.
func arg(vars map[string]string, name string) string {
	return strings.TrimSpace(vars[name])
}

// Expands a variable into whitespace-separated arguments.
func args(vars map[string]string, name string) []string {
	return strings.Fields(vars[name])
}

// A command with its empty arguments dropped
func command(parts ...string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
