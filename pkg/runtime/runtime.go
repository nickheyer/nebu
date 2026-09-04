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

type prebuiltRule struct {
	spec *v1.PrebuiltRule
	when *eval.Expr
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
	prebuilt       []prebuiltRule
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
	for _, p := range m.GetAcquire().GetPrebuilt() {
		when, err := eval.Compile(p.GetWhen())
		if err != nil {
			return nil, fmt.Errorf("prebuilt rule %q: %w", p.GetAsset(), err)
		}
		if _, err := regexp.Compile(p.GetAsset()); err != nil {
			return nil, fmt.Errorf("prebuilt rule %q: %w", p.GetAsset(), err)
		}
		rt.prebuilt = append(rt.prebuilt, prebuiltRule{spec: p, when: when})
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

// Returns the first prebuilt rule that holds on the host
func (rt *Runtime) Prebuilt(profile *v1.HostProfile) (*v1.PrebuiltRule, error) {
	env := host.Env(profile)
	for _, r := range rt.prebuilt {
		ok, err := r.when.Bool(env)
		if err != nil {
			return nil, fmt.Errorf("prebuilt rule %q: %w", r.spec.GetAsset(), err)
		}
		if ok {
			return r.spec, nil
		}
	}
	return nil, fmt.Errorf("no prebuilt release of %s matches this host, adopt a binary instead", rt.Manifest.GetId())
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
