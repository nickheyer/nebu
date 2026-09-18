// Package recipes knows how every runtime is built from source, one file per recipe.
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
	// The repository whose newest release resolves a latest ref, at the github source
	Releases string
	// The archive holding a ref's tree, for a source fetched as a tarball
	Archive func(ref string) string
	// A git repository cloned at the ref, for a source fetched with git
	Repo string
	// A directory inside the archive holding the tree
	Subdir string
}

// Where the source comes from, in words
func (s Source) String() string {
	switch {
	case s.Releases != "":
		return "github.com/" + s.Releases
	case s.Repo != "":
		return s.Repo
	}
	return "none"
}

// Whether a build fetches a source tree at all, so a ref means something
func (s Source) Fetched() bool {
	return s.Releases != "" || s.Repo != "" || s.Archive != nil
}

// One variable a build takes, the kind people set
type Var struct {
	Name string
	// The variable in words
	Label       string
	Default     string
	Description string
	// Values the variable takes, any when empty
	Choices []string
}

// One way a recipe builds, picked by what the host has
type Variant struct {
	ID          string
	Description string
	// Tools the variant needs on the host beyond the recipe's own
	Tools []string
	// What the host must have for the variant, in words; empty when any host takes it
	Requires string
	// Whether the host can take the variant
	Applies func(h *v1.HostProfile) bool
	// Variables the variant sets for this host
	Vars func(h *v1.HostProfile) map[string]string
	// The container image the variant builds in, for the oci sandbox
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

// What one build resolved to, read by the steps
type Build struct {
	Ref     string
	Commit  string
	Variant string
	Vars    map[string]string
	// Paths as the sandbox sees them: the build root, the source tree, and where outputs go
	Root, Src, Out string
	// Parallel jobs the host affords
	Jobs int
	Host *v1.HostProfile
}

// One command run inside the sandbox
type Step struct {
	Name    string
	Command []string
	Env     map[string]string
	// A directory under the source tree to run in, the tree itself when empty
	Dir string
}

// One unified diff applied to the tree
type Patch struct {
	ID string
	// Whether the patch applies to this build, nil meaning always
	Applies func(b *Build) bool
	// The diff itself, or where to fetch it
	Content []byte
	URL     string
	// Leading path components to strip, one when zero
	Strip int
}

// One recipe: everything a build of a runtime from source needs
type Recipe interface {
	ID() string
	RuntimeID() string
	Description() string
	Source() Source
	// Tools every variant needs on the host
	Tools() []string
	// Host and device facts folded into a build's identity, device.vendor and device.compute_capability say
	Facts() []string
	// Variables a build takes with their defaults, the ones people may set, in the order they are shown
	Vars() []Var
	Variants() []Variant
	Sandbox() Sandbox
	// The steps a build runs in order, given what it resolved to
	Steps(b *Build) []Step
	// Paths under the source tree copied out after the steps, globs allowed
	Outputs() []string
	// The binary among the outputs an install points at, relative to the output directory
	Binary() string
	Timeout() time.Duration
	Patches() []Patch
}

// Every recipe nebu ships
func All() []Recipe {
	return []Recipe{LlamaCpp{}, VLLM{}, SGLang{}, NeMo{}, SDCpp{}}
}

// A variable's value, dropped from a command when empty so an unset flag never lands as ""
func arg(vars map[string]string, name string) string {
	return strings.TrimSpace(vars[name])
}

// A variable's value as the arguments it holds, split on whitespace, none when empty
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
