// Package runtime holds runtime manifests and evaluates them against hosts.
package runtime

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/eval"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Value a solved param takes before planning
const Auto = "auto"

// Grace before a stop escalates when the manifest sets none
const DefaultStopGrace = 15 * time.Second

var (
	// Returned when a runtime id is not known
	ErrUnknownRuntime = errors.New("unknown runtime")
	// Returned when a param override is invalid
	ErrParam = errors.New("invalid param")
)

type constraint struct {
	expr    *eval.Expr
	message string
}

type reportRule struct {
	spec *v1.ReportRule
	re   *regexp.Regexp
}

type prebuiltRule struct {
	spec *v1.PrebuiltRule
	when *eval.Expr
}

// One way to obtain an install, its prebuilt rules compiled
type InstallMethod struct {
	Spec  *v1.InstallMethod
	rules []prebuiltRule
}

// Returns one prebuilt rule by id
func (m InstallMethod) Rule(id string) (*v1.PrebuiltRule, error) {
	for _, r := range m.rules {
		if r.spec.GetId() == id {
			return r.spec, nil
		}
	}
	return nil, fmt.Errorf("%w: install method %s has no build %q", ErrParam, m.Spec.GetId(), id)
}

// Returns the prebuilt rules whose conditions hold on the host, in manifest order
func (m InstallMethod) Rules(profile *v1.HostProfile) ([]*v1.PrebuiltRule, error) {
	env := host.Env(profile)
	var out []*v1.PrebuiltRule
	for _, r := range m.rules {
		ok, err := r.when.Holds(env)
		if err != nil {
			return nil, fmt.Errorf("install method %s build %s: %w", m.Spec.GetId(), r.spec.GetId(), err)
		}
		if ok {
			out = append(out, r.spec)
		}
	}
	return out, nil
}

// Returns the first prebuilt rule whose condition holds on the host, nil when none does
func (m InstallMethod) HostRule(profile *v1.HostProfile) (*v1.PrebuiltRule, error) {
	rules, err := m.Rules(profile)
	if err != nil || len(rules) == 0 {
		return nil, err
	}
	return rules[0], nil
}

// Manifest probe with its compiled pattern
type Probe struct {
	Spec    *v1.CommandProbe
	Match   *regexp.Regexp
	command *eval.Template
}

// Compiled runtime manifest
type Runtime struct {
	Manifest       *v1.RuntimeManifest
	Policy         *estimate.Policy
	constraints    []constraint
	params         map[string]*v1.Param
	launchCommand  *eval.Template
	launchArgs     []*eval.Template
	launchEnv      map[string]*eval.Template
	report         []reportRule
	installs       []InstallMethod
	probes         []Probe
	prepareCommand *eval.Template
	prepareArgs    []*eval.Template
	prepareEnv     map[string]*eval.Template
}

// Runtimes ordered by id
type Registry struct {
	list []*Runtime
	byID map[string]*Runtime
}

// Compiles every manifest
func New(manifests []*v1.RuntimeManifest) (*Registry, error) {
	r := &Registry{byID: map[string]*Runtime{}}
	for _, m := range manifests {
		rt, err := compile(m)
		if err != nil {
			return nil, fmt.Errorf("runtime %s: %w", m.GetId(), err)
		}
		r.list = append(r.list, rt)
		r.byID[m.GetId()] = rt
	}
	sort.Slice(r.list, func(i, j int) bool { return r.list[i].Manifest.GetId() < r.list[j].Manifest.GetId() })
	return r, nil
}

func compile(m *v1.RuntimeManifest) (*Runtime, error) {
	rt := &Runtime{Manifest: m, params: map[string]*v1.Param{}}
	for _, c := range m.GetConstraints() {
		e, err := eval.Compile(c.GetExpr())
		if err != nil {
			return nil, err
		}
		rt.constraints = append(rt.constraints, constraint{expr: e, message: c.GetMessage()})
	}
	for _, p := range m.GetParams() {
		if p.GetName() == "" {
			return nil, fmt.Errorf("param without name")
		}
		if _, dup := rt.params[p.GetName()]; dup {
			return nil, fmt.Errorf("duplicate param %s", p.GetName())
		}
		if _, err := convert(p, p.GetDefault()); err != nil {
			return nil, err
		}
		if err := checkForm(p); err != nil {
			return nil, err
		}
		rt.params[p.GetName()] = p
	}
	if m.GetEstimate() != nil {
		policy, err := estimate.NewPolicy(m.GetEstimate())
		if err != nil {
			return nil, err
		}
		rt.Policy = policy
	}
	launch := m.GetLaunch()
	command := launch.GetCommand()
	if command == "" {
		command = "{{.install.path}}"
	}
	var err error
	if rt.launchCommand, err = eval.CompileTemplate(command); err != nil {
		return nil, err
	}
	for _, a := range launch.GetArgs() {
		t, err := eval.CompileTemplate(a)
		if err != nil {
			return nil, err
		}
		rt.launchArgs = append(rt.launchArgs, t)
	}
	if rt.launchEnv, err = eval.CompileTemplates(launch.GetEnv()); err != nil {
		return nil, err
	}
	if prep := launch.GetPrepare(); prep != nil {
		if prep.GetCommand() == "" || len(prep.GetFormats()) == 0 {
			return nil, fmt.Errorf("prepare needs a command and at least one format")
		}
		if rt.prepareCommand, err = eval.CompileTemplate(prep.GetCommand()); err != nil {
			return nil, err
		}
		for _, a := range prep.GetArgs() {
			t, err := eval.CompileTemplate(a)
			if err != nil {
				return nil, err
			}
			rt.prepareArgs = append(rt.prepareArgs, t)
		}
		if rt.prepareEnv, err = eval.CompileTemplates(prep.GetEnv()); err != nil {
			return nil, err
		}
	}
	for _, r := range m.GetReport() {
		re, err := regexp.Compile(r.GetMatch())
		if err != nil {
			return nil, fmt.Errorf("report rule %s: %w", r.GetKey(), err)
		}
		rt.report = append(rt.report, reportRule{spec: r, re: re})
	}
	seen := map[string]bool{}
	for _, im := range m.GetAcquire().GetMethods() {
		id := im.GetId()
		if id == "" {
			return nil, fmt.Errorf("install method without id")
		}
		if seen[id] {
			return nil, fmt.Errorf("install method %s: duplicate id", id)
		}
		seen[id] = true
		method := InstallMethod{Spec: im}
		switch how := im.GetHow().(type) {
		case *v1.InstallMethod_Adopt:
			if len(how.Adopt.GetNames()) == 0 {
				return nil, fmt.Errorf("install method %s: adopt needs at least one binary name", id)
			}
		case *v1.InstallMethod_Prebuilt:
			p := how.Prebuilt
			if p.GetReleases() == "" || len(p.GetRules()) == 0 {
				return nil, fmt.Errorf("install method %s: prebuilt needs releases and at least one rule", id)
			}
			ids := map[string]bool{}
			for _, r := range p.GetRules() {
				if r.GetId() == "" || ids[r.GetId()] {
					return nil, fmt.Errorf("install method %s: every rule needs its own id", id)
				}
				ids[r.GetId()] = true
				if len(r.GetAssets()) == 0 || r.GetBinary() == "" {
					return nil, fmt.Errorf("install method %s build %s: needs at least one asset and a binary", id, r.GetId())
				}
				for _, a := range r.GetAssets() {
					if _, err := regexp.Compile(a); err != nil {
						return nil, fmt.Errorf("install method %s build %s: %w", id, r.GetId(), err)
					}
				}
				when, err := eval.CompileOptional(r.GetWhen())
				if err != nil {
					return nil, fmt.Errorf("install method %s build %s: %w", id, r.GetId(), err)
				}
				method.rules = append(method.rules, prebuiltRule{spec: r, when: when})
			}
		case *v1.InstallMethod_Recipe:
			if how.Recipe.GetRecipeId() == "" {
				return nil, fmt.Errorf("install method %s: recipe needs a recipe_id", id)
			}
		default:
			return nil, fmt.Errorf("install method %s: one of adopt, prebuilt, or recipe required", id)
		}
		rt.installs = append(rt.installs, method)
	}
	for _, p := range m.GetProbes() {
		if p.GetKey() == "" {
			return nil, fmt.Errorf("probe without key")
		}
		re, err := regexp.Compile(p.GetMatch())
		if err != nil {
			return nil, fmt.Errorf("probe %s: %w", p.GetKey(), err)
		}
		probe := Probe{Spec: p, Match: re}
		if p.GetCommand() != "" {
			if probe.command, err = eval.CompileTemplate(p.GetCommand()); err != nil {
				return nil, fmt.Errorf("probe %s: %w", p.GetKey(), err)
			}
		}
		rt.probes = append(rt.probes, probe)
	}
	return rt, nil
}

// Checks the form fields of a param: bounds only on numbers, in order, with a positive step
func checkForm(p *v1.Param) error {
	numeric := p.GetType() == v1.ParamType_PARAM_TYPE_INT || p.GetType() == v1.ParamType_PARAM_TYPE_FLOAT
	if !numeric && (p.GetMin() != 0 || p.GetMax() != 0 || p.GetStep() != 0) {
		return fmt.Errorf("param %s: min, max, and step apply to int and float params only", p.GetName())
	}
	if p.GetStep() < 0 {
		return fmt.Errorf("param %s: step must be positive", p.GetName())
	}
	if p.GetMax() != 0 && p.GetMin() > p.GetMax() {
		return fmt.Errorf("param %s: min %g is above max %g", p.GetName(), p.GetMin(), p.GetMax())
	}
	if p.GetType() == v1.ParamType_PARAM_TYPE_INT {
		for _, v := range []float64{p.GetMin(), p.GetMax(), p.GetStep()} {
			if v != float64(int64(v)) {
				return fmt.Errorf("param %s: int bounds and step must be whole numbers", p.GetName())
			}
		}
	}
	return nil
}

// Returns the install methods in manifest order
func (rt *Runtime) Installs() []InstallMethod { return rt.installs }

// Returns one install method by id
func (rt *Runtime) Install(id string) (InstallMethod, error) {
	for _, m := range rt.installs {
		if m.Spec.GetId() == id {
			return m, nil
		}
	}
	return InstallMethod{}, fmt.Errorf("%w: %s has no install method %q", ErrParam, rt.Manifest.GetId(), id)
}

// The ids of the recipes the manifest builds from, in order, each once
func (rt *Runtime) RecipeIDs() []string {
	var out []string
	for _, m := range rt.installs {
		if id := m.Spec.GetRecipe().GetRecipeId(); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// Returns the manifest probes with compiled patterns
func (rt *Runtime) Probes() []Probe { return rt.probes }

// Returns the grace period before a stop escalates
func (rt *Runtime) StopGrace() time.Duration {
	if ms := rt.Manifest.GetLaunch().GetStopGraceMs(); ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return DefaultStopGrace
}

// Lists runtimes by id
func (r *Registry) List() []*Runtime { return r.list }

// Returns the wire format a runtime's server speaks, OpenAI when the manifest says nothing
func (r *Registry) API(id string) v1.ApiFlavor {
	if r != nil {
		if rt, ok := r.byID[id]; ok && rt.Manifest.GetLaunch().GetApi() != v1.ApiFlavor_API_FLAVOR_UNSPECIFIED {
			return rt.Manifest.GetLaunch().GetApi()
		}
	}
	return v1.ApiFlavor_API_FLAVOR_OPENAI
}

// Returns one runtime by id
func (r *Registry) Get(id string) (*Runtime, error) {
	rt, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownRuntime, id)
	}
	return rt, nil
}

// Reports whether the runtime accepts a format
func (rt *Runtime) Accepts(formatID string) bool {
	return slices.Contains(rt.Manifest.GetFormats(), formatID)
}

// Reports whether the runtime prepares groups of a format before launching them
func (rt *Runtime) Prepares(formatID string) bool {
	return rt.prepareCommand != nil && slices.Contains(rt.Manifest.GetLaunch().GetPrepare().GetFormats(), formatID)
}

// Evaluates constraints against a host profile
func (rt *Runtime) Compatible(profile *v1.HostProfile) (bool, []string) {
	env := host.Env(profile)
	var unmet []string
	for _, c := range rt.constraints {
		ok, err := c.expr.Bool(env)
		if err != nil {
			unmet = append(unmet, fmt.Sprintf("%s: %v", c.message, err))
			continue
		}
		if !ok {
			unmet = append(unmet, c.message)
		}
	}
	return len(unmet) == 0, unmet
}

// Builds status for API responses
func (rt *Runtime) Status(profile *v1.HostProfile) *v1.RuntimeStatus {
	ok, unmet := rt.Compatible(profile)
	return &v1.RuntimeStatus{Manifest: rt.Manifest, Compatible: ok, Unmet: unmet}
}

// Layers param maps, later ones over earlier ones, into a fresh map
func Merge(layers ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, l := range layers {
		for k, v := range l {
			out[k] = v
		}
	}
	return out
}

// Resolves typed params from defaults and overrides
func (rt *Runtime) Params(overrides map[string]string) (map[string]any, error) {
	out := make(map[string]any, len(rt.params))
	for name, p := range rt.params {
		v, err := convert(p, p.GetDefault())
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	for name, raw := range overrides {
		p, ok := rt.params[name]
		if !ok {
			return nil, fmt.Errorf("%w: unknown param %q for runtime %s", ErrParam, name, rt.Manifest.GetId())
		}
		v, err := convert(p, raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrParam, err)
		}
		out[name] = v
	}
	return out, nil
}

func convert(p *v1.Param, raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	if p.GetSolved() && (raw == "" || strings.EqualFold(raw, Auto)) {
		return Auto, nil
	}
	if len(p.GetChoices()) > 0 && !slices.Contains(p.GetChoices(), raw) {
		return nil, fmt.Errorf("param %s: %q not in %v", p.GetName(), raw, p.GetChoices())
	}
	switch p.GetType() {
	case v1.ParamType_PARAM_TYPE_INT:
		if raw == "" {
			return int64(0), nil
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("param %s: %w", p.GetName(), err)
		}
		return n, nil
	case v1.ParamType_PARAM_TYPE_FLOAT:
		if raw == "" {
			return float64(0), nil
		}
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("param %s: %w", p.GetName(), err)
		}
		return f, nil
	case v1.ParamType_PARAM_TYPE_BOOL:
		if raw == "" {
			return false, nil
		}
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("param %s: %w", p.GetName(), err)
		}
		return b, nil
	}
	return raw, nil
}

// Sums allocations the runtime reported, keyed by rule
func (rt *Runtime) Measure(lines []string) []*v1.Measurement {
	var out []*v1.Measurement
	byKey := map[string]*v1.Measurement{}
	for _, r := range rt.report {
		for _, line := range lines {
			m := r.re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			var value, unit string
			for i, name := range r.re.SubexpNames() {
				switch name {
				case "value":
					value = m[i]
				case "unit":
					unit = m[i]
				}
			}
			if unit == "" {
				unit = r.spec.GetUnit()
			}
			bytes, err := eval.Bytes(value, unit)
			if err != nil {
				continue
			}
			ms, ok := byKey[r.spec.GetKey()]
			if !ok {
				ms = &v1.Measurement{Key: r.spec.GetKey(), Line: line}
				byKey[r.spec.GetKey()] = ms
				out = append(out, ms)
			}
			ms.Bytes += bytes
		}
	}
	return out
}
