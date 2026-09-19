// Package build turns recipes into installs through sandboxed steps.
package build

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/build/sandbox"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/recipes"
	"github.com/nickheyer/nebu/pkg/text"
)

const (
	// Ref value meaning the newest release in the feed
	Latest = "latest"
	// Build timeout when the recipe sets none
	DefaultTimeout = 2 * time.Hour
	// Variant id used when a recipe declares none
	DefaultVariant = "default"
	srcDir         = "src"
	outDir         = "out"
)

// Container CLIs tried in order when neither the recipe nor config names any
var defaultCLIs = []string{"podman", "docker", "nerdctl"}

var (
	// Returned when a recipe id is not known
	ErrUnknownRecipe = errors.New("unknown recipe")
	// Returned when a request names something the recipe lacks
	ErrSelection = errors.New("invalid build selection")
)

// Recipes ordered by id
type Registry struct {
	list []recipes.Recipe
	byID map[string]recipes.Recipe
}

// Indexes every recipe
func New(list []recipes.Recipe) (*Registry, error) {
	r := &Registry{byID: map[string]recipes.Recipe{}}
	for _, rc := range list {
		if rc.ID() == "" {
			return nil, fmt.Errorf("recipe without id")
		}
		if rc.RuntimeID() == "" {
			return nil, fmt.Errorf("recipe %s: runtime id required", rc.ID())
		}
		if strings.TrimSpace(rc.Binary()) == "" {
			return nil, fmt.Errorf("recipe %s: binary required", rc.ID())
		}
		if _, dup := r.byID[rc.ID()]; dup {
			return nil, fmt.Errorf("recipe %s: duplicate id", rc.ID())
		}
		for _, v := range rc.Variants() {
			if v.ID == "" || v.Applies == nil {
				return nil, fmt.Errorf("recipe %s: every variant needs an id and an applies rule", rc.ID())
			}
		}
		r.list = append(r.list, rc)
		r.byID[rc.ID()] = rc
	}
	sort.Slice(r.list, func(i, j int) bool { return r.list[i].ID() < r.list[j].ID() })
	return r, nil
}

// Lists recipes by id
func (r *Registry) List() []recipes.Recipe { return r.list }

// Returns one recipe by id
func (r *Registry) Get(id string) (recipes.Recipe, error) {
	rc, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownRecipe, id)
	}
	return rc, nil
}

// Lists recipes of one runtime
func (r *Registry) ForRuntime(runtimeID string) []recipes.Recipe {
	var out []recipes.Recipe
	for _, rc := range r.list {
		if rc.RuntimeID() == runtimeID {
			out = append(out, rc)
		}
	}
	return out
}

// Build recipe and supported variants for the host.
func Describe(rc recipes.Recipe, profile *v1.HostProfile) *v1.Recipe {
	out := &v1.Recipe{
		Id:          rc.ID(),
		RuntimeId:   rc.RuntimeID(),
		Description: rc.Description(),
		Source:      rc.Source().String(),
		Tools:       rc.Tools(),
		Sandbox:     rc.Sandbox().Kind,
		SandboxClis: rc.Sandbox().CLIs,
		TimeoutMs:   uint32(Timeout(rc) / time.Millisecond),
	}
	for _, v := range rc.Vars() {
		out.Vars = append(out.Vars, &v1.Var{Name: v.Name, Label: v.Label, Default: v.Default, Description: v.Description, Choices: v.Choices})
	}
	for _, v := range rc.Variants() {
		pv := &v1.Variant{Id: v.ID, Description: v.Description, Tools: v.Tools, Requires: v.Requires}
		if v.Applies(profile) {
			pv.Vars = variantVars(v, profile)
		}
		out.Variants = append(out.Variants, pv)
	}
	return out
}

// Returns trimmed, nonempty variant variables.
func variantVars(v recipes.Variant, profile *v1.HostProfile) map[string]string {
	if v.Vars == nil {
		return nil
	}
	out := map[string]string{}
	for k, val := range v.Vars(profile) {
		if val = strings.TrimSpace(val); val != "" {
			out[k] = val
		}
	}
	return out
}

// Chooses the variant image, recipe image, or configured default, in that order.
func Image(rc recipes.Recipe, v recipes.Variant, defaults *v1.Builds) string {
	if v.Image != "" {
		return v.Image
	}
	if img := rc.Sandbox().Image; img != "" {
		return img
	}
	return defaults.GetImage()
}

// Chooses recipe CLIs, configured CLIs, or podman, docker, and nerdctl.
func CLIs(rc recipes.Recipe, defaults *v1.Builds) []string {
	if clis := rc.Sandbox().CLIs; len(clis) > 0 {
		return clis
	}
	if clis := defaults.GetCli(); len(clis) > 0 {
		return clis
	}
	return defaultCLIs
}

// Unique required tools in recipe order.
func allTools(rc recipes.Recipe) []string {
	var out []string
	seen := map[string]bool{}
	add := func(tools []string) {
		for _, t := range tools {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	add(rc.Tools())
	for _, v := range rc.Variants() {
		add(v.Tools)
	}
	return out
}

// Returns the timeout for one build
func Timeout(rc recipes.Recipe) time.Duration {
	if t := rc.Timeout(); t > 0 {
		return t
	}
	return DefaultTimeout
}

// Returns the variants the host can take, in recipe order
func Variants(rc recipes.Recipe, profile *v1.HostProfile) []recipes.Variant {
	var out []recipes.Variant
	for _, v := range rc.Variants() {
		if v.Applies(profile) {
			out = append(out, v)
		}
	}
	return out
}

// Caller choices and site defaults for one build
type Options struct {
	Variant  string
	Vars     map[string]string
	Sandbox  v1.SandboxKind
	Image    string
	Ref      string
	Defaults *v1.Builds
}

// Resolved build selection.
type Selection struct {
	Recipe  recipes.Recipe
	Variant recipes.Variant
	Vars    map[string]string
	Sandbox v1.SandboxKind
	// Chooses recipe CLIs, configured CLIs, or podman, docker, and nerdctl.
	CLIs  []string
	CLI   string
	Image string
	Ref   string
	Facts map[string]string
	// Tools the chosen variant needs that the host lacks, under the host sandbox
	MissingTools []string
	// Tools any variant needs that the host lacks
	AbsentTools []string
	Unmet       []string
	profile     *v1.HostProfile
	overrides   map[string]string
}

// Evaluates variants, sandbox, tools, vars, and facts against the host
func Select(rc recipes.Recipe, profile *v1.HostProfile, opts Options) (*Selection, error) {
	sel := &Selection{Recipe: rc, profile: profile, Vars: map[string]string{}, Facts: map[string]string{}, overrides: opts.Vars}
	sel.Sandbox = rc.Sandbox().Kind
	if k := opts.Defaults.GetSandbox(); k != v1.SandboxKind_SANDBOX_KIND_UNSPECIFIED {
		sel.Sandbox = k
	}
	if opts.Sandbox != v1.SandboxKind_SANDBOX_KIND_UNSPECIFIED {
		sel.Sandbox = opts.Sandbox
	}
	if sel.Sandbox == v1.SandboxKind_SANDBOX_KIND_UNSPECIFIED {
		sel.Sandbox = v1.SandboxKind_SANDBOX_KIND_HOST
	}
	if err := sel.pickVariant(opts.Variant); err != nil {
		return nil, err
	}
	sel.Ref = opts.Ref
	if sel.Ref == "" && rc.Source().Releases != "" {
		sel.Ref = Latest
	}
	sel.resolveVars()
	sel.CLIs = CLIs(rc, opts.Defaults)
	sel.CLI, _ = sandbox.Detect(sel.CLIs)
	sel.AbsentTools = sandbox.MissingTools(allTools(rc))
	if sel.Sandbox == v1.SandboxKind_SANDBOX_KIND_OCI {
		sel.Image = Image(rc, sel.Variant, opts.Defaults)
		if opts.Image != "" {
			sel.Image = opts.Image
		}
		if sel.CLI == "" {
			sel.Unmet = append(sel.Unmet, "no container cli among "+strings.Join(sel.CLIs, ", "))
		}
		if sel.Image == "" {
			sel.Unmet = append(sel.Unmet, "no container image, pass --image or set builds.image")
		}
	} else {
		sel.MissingTools = sandbox.MissingTools(append(append([]string(nil), rc.Tools()...), sel.Variant.Tools...))
		if len(sel.MissingTools) > 0 {
			sel.Unmet = append(sel.Unmet, "missing tools "+strings.Join(sel.MissingTools, ", "))
		}
	}
	src := rc.Source()
	if src.Repo != "" && src.Archive == nil {
		if missing := sandbox.MissingTools([]string{"git"}); len(missing) > 0 {
			sel.Unmet = append(sel.Unmet, "git is needed on the host to clone the source")
		}
	}
	sel.Facts = hashFacts(profile, rc.Facts())
	return sel, nil
}

// Combines recipe defaults, caller overrides, and variant values, in that order.
func (s *Selection) resolveVars() {
	s.Vars = map[string]string{}
	for _, v := range s.Recipe.Vars() {
		s.Vars[v.Name] = strings.TrimSpace(v.Default)
	}
	for k, v := range s.overrides {
		s.Vars[k] = strings.TrimSpace(v)
	}
	if s.Variant.Vars != nil {
		for k, v := range s.Variant.Vars(s.profile) {
			s.Vars[k] = strings.TrimSpace(v)
		}
	}
}

func (s *Selection) pickVariant(want string) error {
	rc := s.Recipe
	variants := rc.Variants()
	if len(variants) == 0 {
		if want != "" && want != DefaultVariant {
			return fmt.Errorf("%w: recipe %s has no variants", ErrSelection, rc.ID())
		}
		s.Variant = recipes.Variant{ID: DefaultVariant, Applies: func(*v1.HostProfile) bool { return true }}
		return nil
	}
	if want != "" {
		for _, v := range variants {
			if v.ID == want {
				s.Variant = v
				return nil
			}
		}
		return fmt.Errorf("%w: recipe %s has no variant %q", ErrSelection, rc.ID(), want)
	}
	var first *recipes.Variant
	for i := range variants {
		v := variants[i]
		if !v.Applies(s.profile) {
			continue
		}
		if first == nil {
			first = &v
		}
		if s.Sandbox != v1.SandboxKind_SANDBOX_KIND_HOST || len(sandbox.MissingTools(append(append([]string(nil), rc.Tools()...), v.Tools...))) == 0 {
			s.Variant = v
			return nil
		}
	}
	if first == nil {
		return fmt.Errorf("%w: no variant of recipe %s matches this host, pass --variant", ErrSelection, rc.ID())
	}
	s.Variant = *first
	return nil
}

// Reports the selection for the API
func (s *Selection) Status() *v1.RecipeStatus {
	return &v1.RecipeStatus{
		Recipe:       Describe(s.Recipe, s.profile),
		Variant:      s.Variant.ID,
		MissingTools: s.MissingTools,
		AbsentTools:  s.AbsentTools,
		Sandbox:      s.Sandbox,
		SandboxCli:   s.CLI,
		Unmet:        s.Unmet,
		Vars:         s.Vars,
	}
}

// Returns why the selection cannot run, nil when it can
func (s *Selection) Err() error {
	if len(s.Unmet) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrSelection, strings.Join(s.Unmet, ". "))
}

// Collects the host and device facts folded into the hash
func hashFacts(profile *v1.HostProfile, keys []string) map[string]string {
	out := map[string]string{}
	for _, key := range keys {
		if v, ok := profile.GetFacts()[key]; ok {
			out[key] = v
			continue
		}
		var values []string
		for _, d := range profile.GetDevices() {
			switch key {
			case "device.vendor":
				values = append(values, d.GetVendor())
			case "device.name":
				values = append(values, d.GetName())
			case "device.kind":
				values = append(values, text.Enum(d.GetKind()))
			default:
				if v, ok := d.GetFacts()[strings.TrimPrefix(key, "device.")]; ok {
					values = append(values, v)
				}
			}
		}
		if len(values) > 0 {
			sort.Strings(values)
			out[key] = strings.Join(values, ",")
		}
	}
	return out
}
