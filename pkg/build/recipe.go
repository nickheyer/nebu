// Package build turns recipes into installs through sandboxed steps.
package build

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/build/sandbox"
	"github.com/nickheyer/nebu/pkg/eval"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Source id releases are read from when a recipe or rule names none
const DefaultReleaseSource = "github"

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

// Container CLIs tried in order when config names none
var defaultCLIs = []string{"podman", "docker", "nerdctl"}

var (
	// Returned when a recipe id is not known
	ErrUnknownRecipe = errors.New("unknown recipe")
	// Returned when a request names something the recipe lacks
	ErrSelection = errors.New("invalid build selection")
)

type patch struct {
	spec *v1.Patch
	when *eval.Expr
}

type variant struct {
	spec  *v1.BuildVariant
	when  *eval.Expr
	vars  map[string]*eval.Template
	image *eval.Template
}

type step struct {
	spec    *v1.BuildStep
	when    *eval.Expr
	command []*eval.Template
	env     map[string]*eval.Template
	dir     *eval.Template
}

// Compiled recipe
type Recipe struct {
	Spec         *v1.Recipe
	releases     *eval.Template
	source       *eval.Template
	ref          *eval.Template
	archive      *eval.Template
	repo         *eval.Template
	subdir       *eval.Template
	patches      []patch
	variants     []variant
	steps        []step
	outputs      []*eval.Template
	binary       *eval.Template
	vars         map[string]*eval.Template
	sandboxImage *eval.Template
	sandboxArgs  []*eval.Template
}

// Recipes ordered by id
type Registry struct {
	list []*Recipe
	byID map[string]*Recipe
}

// Compiles every recipe
func New(specs []*v1.Recipe) (*Registry, error) {
	r := &Registry{byID: map[string]*Recipe{}}
	for _, s := range specs {
		rc, err := compile(s)
		if err != nil {
			return nil, fmt.Errorf("recipe %s: %w", s.GetId(), err)
		}
		if _, dup := r.byID[s.GetId()]; dup {
			return nil, fmt.Errorf("recipe %s: duplicate id", s.GetId())
		}
		r.list = append(r.list, rc)
		r.byID[s.GetId()] = rc
	}
	sort.Slice(r.list, func(i, j int) bool { return r.list[i].Spec.GetId() < r.list[j].Spec.GetId() })
	return r, nil
}

// Lists recipes by id
func (r *Registry) List() []*Recipe { return r.list }

// Returns one recipe by id
func (r *Registry) Get(id string) (*Recipe, error) {
	rc, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownRecipe, id)
	}
	return rc, nil
}

// Lists recipes of one runtime
func (r *Registry) ForRuntime(runtimeID string) []*Recipe {
	var out []*Recipe
	for _, rc := range r.list {
		if rc.Spec.GetRuntimeId() == runtimeID {
			out = append(out, rc)
		}
	}
	return out
}

func optionalTemplate(src string) (*eval.Template, error) {
	if strings.TrimSpace(src) == "" {
		return nil, nil
	}
	return eval.CompileTemplate(src)
}

func templateList(srcs []string) ([]*eval.Template, error) {
	out := make([]*eval.Template, 0, len(srcs))
	for _, s := range srcs {
		t, err := eval.CompileTemplate(s)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func compile(s *v1.Recipe) (*Recipe, error) {
	if s.GetRuntimeId() == "" {
		return nil, fmt.Errorf("runtime_id required")
	}
	if strings.TrimSpace(s.GetBinary()) == "" {
		return nil, fmt.Errorf("binary required")
	}
	if len(s.GetSteps()) == 0 {
		return nil, fmt.Errorf("at least one step required")
	}
	rc := &Recipe{Spec: s}
	var err error
	src := s.GetSource()
	if rc.releases, err = optionalTemplate(src.GetReleases()); err != nil {
		return nil, err
	}
	if rc.source, err = optionalTemplate(src.GetSource()); err != nil {
		return nil, err
	}
	if rc.ref, err = optionalTemplate(src.GetRef()); err != nil {
		return nil, err
	}
	if rc.archive, err = optionalTemplate(src.GetArchive()); err != nil {
		return nil, err
	}
	if rc.repo, err = optionalTemplate(src.GetRepo()); err != nil {
		return nil, err
	}
	if rc.subdir, err = optionalTemplate(src.GetSubdir()); err != nil {
		return nil, err
	}
	for _, p := range s.GetPatches() {
		if p.GetId() == "" {
			return nil, fmt.Errorf("patch without id")
		}
		if p.GetContent() == "" && p.GetFile() == "" && p.GetUrl() == "" {
			return nil, fmt.Errorf("patch %s: content, file, or url required", p.GetId())
		}
		when, err := eval.CompileOptional(p.GetWhen())
		if err != nil {
			return nil, fmt.Errorf("patch %s: %w", p.GetId(), err)
		}
		rc.patches = append(rc.patches, patch{spec: p, when: when})
	}
	for _, v := range s.GetVariants() {
		if v.GetId() == "" {
			return nil, fmt.Errorf("variant without id")
		}
		when, err := eval.CompileOptional(v.GetWhen())
		if err != nil {
			return nil, fmt.Errorf("variant %s: %w", v.GetId(), err)
		}
		vars, err := eval.CompileTemplates(v.GetVars())
		if err != nil {
			return nil, fmt.Errorf("variant %s: %w", v.GetId(), err)
		}
		image, err := optionalTemplate(v.GetImage())
		if err != nil {
			return nil, fmt.Errorf("variant %s: %w", v.GetId(), err)
		}
		rc.variants = append(rc.variants, variant{spec: v, when: when, vars: vars, image: image})
	}
	for i, st := range s.GetSteps() {
		name := st.GetName()
		if name == "" {
			name = fmt.Sprintf("step %d", i+1)
		}
		if len(st.GetCommand()) == 0 {
			return nil, fmt.Errorf("%s: command required", name)
		}
		when, err := eval.CompileOptional(st.GetWhen())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		command, err := templateList(st.GetCommand())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		env, err := eval.CompileTemplates(st.GetEnv())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		dir, err := optionalTemplate(st.GetDir())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		rc.steps = append(rc.steps, step{spec: st, when: when, command: command, env: env, dir: dir})
	}
	if rc.outputs, err = templateList(s.GetOutputs()); err != nil {
		return nil, err
	}
	if rc.binary, err = eval.CompileTemplate(s.GetBinary()); err != nil {
		return nil, err
	}
	if rc.vars, err = eval.CompileTemplates(s.GetVars()); err != nil {
		return nil, err
	}
	if rc.sandboxImage, err = optionalTemplate(s.GetSandbox().GetImage()); err != nil {
		return nil, err
	}
	if rc.sandboxArgs, err = templateList(s.GetSandbox().GetArgs()); err != nil {
		return nil, err
	}
	return rc, nil
}

// Returns the timeout for one build
func (rc *Recipe) Timeout() time.Duration {
	if ms := rc.Spec.GetTimeoutMs(); ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return DefaultTimeout
}

// Returns the variants whose conditions hold on the host, in recipe order
func (rc *Recipe) Variants(profile *v1.HostProfile) ([]*v1.BuildVariant, error) {
	env := host.Env(profile)
	var out []*v1.BuildVariant
	for _, v := range rc.variants {
		ok, err := v.when.Holds(env)
		if err != nil {
			return nil, fmt.Errorf("variant %s: %w", v.spec.GetId(), err)
		}
		if ok {
			out = append(out, v.spec)
		}
	}
	return out, nil
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

// What the host selects for a recipe, ready to run
type Selection struct {
	Recipe       *Recipe
	Variant      *v1.BuildVariant
	Vars         map[string]string
	Sandbox      v1.SandboxKind
	CLI          string
	Image        string
	Ref          string
	Facts        map[string]string
	MissingTools []string
	Unmet        []string
	env          map[string]any
	overrides    map[string]string
}

// Evaluates variants, sandbox, tools, vars, and facts against the host
func (rc *Recipe) Select(profile *v1.HostProfile, opts Options) (*Selection, error) {
	sel := &Selection{Recipe: rc, env: host.Env(profile), Vars: map[string]string{}, Facts: map[string]string{}}
	sel.Sandbox = rc.Spec.GetSandbox().GetKind()
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
	if sel.Ref == "" && rc.ref != nil {
		ref, err := rc.ref.Render(sel.context())
		if err != nil {
			return nil, err
		}
		sel.Ref = strings.TrimSpace(ref)
	}
	if sel.Ref == "" && rc.releases != nil {
		sel.Ref = Latest
	}
	sel.overrides = opts.Vars
	if err := sel.renderVars(); err != nil {
		return nil, err
	}
	var err error
	ctx := sel.context()
	if sel.Sandbox == v1.SandboxKind_SANDBOX_KIND_OCI {
		if sel.Image == "" && rc.sandboxImage != nil {
			if sel.Image, err = rc.sandboxImage.Render(ctx); err != nil {
				return nil, err
			}
		}
		if sel.Image == "" {
			sel.Image = opts.Defaults.GetImage()
		}
		if opts.Image != "" {
			sel.Image = opts.Image
		}
		clis := rc.Spec.GetSandbox().GetCli()
		if len(clis) == 0 {
			clis = opts.Defaults.GetCli()
		}
		if len(clis) == 0 {
			clis = defaultCLIs
		}
		cli, ok := sandbox.Detect(clis)
		if !ok {
			sel.Unmet = append(sel.Unmet, "no container cli among "+strings.Join(clis, ", "))
		}
		sel.CLI = cli
		if sel.Image == "" {
			sel.Unmet = append(sel.Unmet, "no container image, pass --image or set builds.image")
		}
	} else {
		sel.Image = ""
		tools := append(append([]string(nil), rc.Spec.GetTools()...), sel.Variant.GetTools()...)
		sel.MissingTools = sandbox.MissingTools(tools)
		if len(sel.MissingTools) > 0 {
			sel.Unmet = append(sel.Unmet, "missing tools "+strings.Join(sel.MissingTools, ", "))
		}
	}
	if rc.repo != nil && rc.archive == nil {
		if missing := sandbox.MissingTools([]string{"git"}); len(missing) > 0 {
			sel.Unmet = append(sel.Unmet, "git is needed on the host to clone the source")
		}
	}
	sel.Facts = hashFacts(profile, rc.Spec.GetFacts())
	return sel, nil
}

// Renders recipe vars then variant vars against the ref, overrides seeded first
func (s *Selection) renderVars() error {
	s.Vars = map[string]string{}
	for k, v := range s.overrides {
		s.Vars[k] = v
	}
	if err := s.renderVarMap(s.Recipe.vars); err != nil {
		return err
	}
	for _, v := range s.Recipe.variants {
		if v.spec.GetId() != s.Variant.GetId() {
			continue
		}
		if err := s.renderVarMap(v.vars); err != nil {
			return fmt.Errorf("variant %s: %w", v.spec.GetId(), err)
		}
		if v.image != nil {
			image, err := v.image.Render(s.context())
			if err != nil {
				return err
			}
			s.Image = strings.TrimSpace(image)
		}
	}
	return nil
}

// Renders vars in key order so later ones reference earlier, skipping overrides
func (s *Selection) renderVarMap(ts map[string]*eval.Template) error {
	keys := make([]string, 0, len(ts))
	for k := range ts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := s.overrides[k]; ok {
			continue
		}
		v, err := ts[k].Render(s.context())
		if err != nil {
			return fmt.Errorf("var %s: %w", k, err)
		}
		s.Vars[k] = strings.TrimSpace(v)
	}
	return nil
}

func (s *Selection) pickVariant(want string) error {
	rc := s.Recipe
	if len(rc.variants) == 0 {
		if want != "" && want != DefaultVariant {
			return fmt.Errorf("%w: recipe %s has no variants", ErrSelection, rc.Spec.GetId())
		}
		s.Variant = &v1.BuildVariant{Id: DefaultVariant}
		return nil
	}
	if want != "" {
		for _, v := range rc.variants {
			if v.spec.GetId() == want {
				s.Variant = v.spec
				return nil
			}
		}
		return fmt.Errorf("%w: recipe %s has no variant %q", ErrSelection, rc.Spec.GetId(), want)
	}
	var first *v1.BuildVariant
	for _, v := range rc.variants {
		ok, err := v.when.Holds(s.env)
		if err != nil {
			return fmt.Errorf("variant %s: %w", v.spec.GetId(), err)
		}
		if !ok {
			continue
		}
		if first == nil {
			first = v.spec
		}
		if s.Sandbox != v1.SandboxKind_SANDBOX_KIND_HOST || len(sandbox.MissingTools(append(append([]string(nil), rc.Spec.GetTools()...), v.spec.GetTools()...))) == 0 {
			s.Variant = v.spec
			return nil
		}
	}
	if first == nil {
		return fmt.Errorf("%w: no variant of recipe %s matches this host, pass --variant", ErrSelection, rc.Spec.GetId())
	}
	s.Variant = first
	return nil
}

// Builds the template context, host env plus build values
func (s *Selection) context() map[string]any {
	ctx := make(map[string]any, len(s.env)+8)
	for k, v := range s.env {
		ctx[k] = v
	}
	ctx["vars"] = s.Vars
	ctx["variant"] = s.Variant.GetId()
	ctx["ref"] = s.Ref
	ctx["commit"] = ""
	ctx["dir"] = ""
	ctx["out"] = ""
	ctx["root"] = ""
	ctx["jobs"] = 1
	return ctx
}

// Reports the selection for the API
func (s *Selection) Status() *v1.RecipeStatus {
	return &v1.RecipeStatus{
		Recipe:       s.Recipe.Spec,
		Variant:      s.Variant.GetId(),
		MissingTools: s.MissingTools,
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
	return fmt.Errorf("%w: %s", ErrSelection, strings.Join(s.Unmet, "; "))
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
				values = append(values, eval.EnumShort(d.GetKind()))
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
