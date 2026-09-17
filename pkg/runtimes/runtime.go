// Package runtimes knows every inference server nebu can install and launch, one file per runtime.
package runtimes

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/triage"
)

// Value a solved param takes before planning
const Auto = estimate.Auto

// Grace before a stop escalates when the runtime sets none
const DefaultStopGrace = 15 * time.Second

// What an auto context length resolves to, the same rule on every runtime the planner solves it for
const contextRule = "the largest context that fits in memory, up to the length the model was trained for"

var (
	// Returned when a runtime id is not known
	ErrUnknownRuntime = errors.New("unknown runtime")
	// Returned when a param override is invalid
	ErrParam = errors.New("invalid param")
)

// A usable copy of a runtime, as a launch or probe reads it
type Install struct {
	Path, Dir, Version string
}

// Everything a launch reads
type Launch struct {
	Name   string
	Params estimate.Params
	// Stored paths by role, weights, projector, and the group directory under weights_dir, plus prepared_dir for a prepared tree
	Artifacts map[string]string
	Host      string
	Port      int
	Install   Install
	// The devices a slot pins the run to, none when the run sees every device
	Devices []*v1.Device
	// Where the slot keeps the model, the host alone meaning no device is touched
	Placement  v1.Placement
	Descriptor *v1.Descriptor
}

// A command line ready to run, with the param values it carries
type Command struct {
	Command string
	Args    []string
	Env     map[string]string
	Params  map[string]string
}

// Readiness probe of a running instance
type Health struct {
	Path     string
	Interval time.Duration
	Timeout  time.Duration
}

// One release asset by the shape of its name
type Asset struct {
	Prefix   string
	Contains string
	Suffix   string
}

// Whether a published file is this asset
func (a Asset) Matches(name string) bool {
	return strings.HasPrefix(name, a.Prefix) && strings.Contains(name, a.Contains) && strings.HasSuffix(name, a.Suffix)
}

// In words, for a person reading which build was chosen
func (a Asset) String() string { return a.Prefix + "*" + a.Contains + "*" + a.Suffix }

// One published build of a release, offered when it applies to the host
type PrebuiltRule struct {
	ID      string
	Applies func(h *v1.HostProfile) bool
	Assets  []Asset
	Binary  string
}

// One way to obtain an install
type Method struct {
	ID          string
	Description string
	Kind        v1.InstallKind
	// Binaries looked for on PATH, for an adopted install
	Binaries []string
	// The repository releases come from and the builds it publishes, for a downloaded install
	Releases string
	Rules    []PrebuiltRule
	// The recipe built, for a built install
	RecipeID string
}

// Runs an install once and reads one fact from what it prints
type Probe struct {
	Key string
	// The command to run, the install itself when nil
	Command func(in Install) string
	Args    []string
	Timeout time.Duration
	// Reads the fact out of the output, false when it is not there
	Parse func(output string) (string, bool)
}

// One inference server: how to obtain it, launch it, probe it, size memory for it, and read its failures
type Runtime interface {
	ID() string
	Name() string
	Description() string
	Formats() []string
	API() v1.ApiFlavor
	// What the host must have, in words
	Requirements() []string
	// The requirements this host does not meet, empty when the runtime can run here
	Unmet(h *v1.HostProfile) []string
	Methods() []Method
	Params() []*v1.Param
	Launch(in Launch) (*Command, error)
	// Whether stored groups of a format go through a step before their first launch
	Prepares(formatID string) bool
	// The step, nil for a format that needs none
	Prepare(in Launch) (*Command, error)
	PrepareTimeout() time.Duration
	Health() Health
	StopGrace() time.Duration
	Policy() *estimate.Policy
	Probes() []Probe
	// Allocations the runtime's log reports, summed by key
	Measure(lines []string) []*v1.Measurement
	Triage() []triage.Set
}

// Runtimes ordered by id
type Registry struct {
	list []Runtime
	byID map[string]Runtime
}

// Indexes every runtime, checking its params
func New(list []Runtime) (*Registry, error) {
	r := &Registry{byID: map[string]Runtime{}}
	for _, rt := range list {
		if rt.ID() == "" {
			return nil, fmt.Errorf("runtime without id")
		}
		if _, dup := r.byID[rt.ID()]; dup {
			return nil, fmt.Errorf("runtime %s: duplicate id", rt.ID())
		}
		seen := map[string]bool{}
		for _, p := range rt.Params() {
			if p.GetName() == "" {
				return nil, fmt.Errorf("runtime %s: param without name", rt.ID())
			}
			if seen[p.GetName()] {
				return nil, fmt.Errorf("runtime %s: duplicate param %s", rt.ID(), p.GetName())
			}
			seen[p.GetName()] = true
			if _, err := convert(p, p.GetDefault()); err != nil {
				return nil, fmt.Errorf("runtime %s: %w", rt.ID(), err)
			}
			if err := checkRule(p); err != nil {
				return nil, fmt.Errorf("runtime %s: %w", rt.ID(), err)
			}
			if err := checkForm(p); err != nil {
				return nil, fmt.Errorf("runtime %s: %w", rt.ID(), err)
			}
		}
		methods := map[string]bool{}
		for _, m := range rt.Methods() {
			if m.ID == "" || methods[m.ID] {
				return nil, fmt.Errorf("runtime %s: every install method needs its own id", rt.ID())
			}
			methods[m.ID] = true
			switch m.Kind {
			case v1.InstallKind_INSTALL_KIND_ADOPTED:
				if len(m.Binaries) == 0 {
					return nil, fmt.Errorf("runtime %s method %s: adopt needs at least one binary name", rt.ID(), m.ID)
				}
			case v1.InstallKind_INSTALL_KIND_PREBUILT:
				if m.Releases == "" || len(m.Rules) == 0 {
					return nil, fmt.Errorf("runtime %s method %s: prebuilt needs releases and at least one rule", rt.ID(), m.ID)
				}
				for _, rule := range m.Rules {
					if rule.ID == "" || len(rule.Assets) == 0 || rule.Binary == "" || rule.Applies == nil {
						return nil, fmt.Errorf("runtime %s method %s: every rule needs an id, assets, a binary, and an applies rule", rt.ID(), m.ID)
					}
				}
			case v1.InstallKind_INSTALL_KIND_BUILT:
				if m.RecipeID == "" {
					return nil, fmt.Errorf("runtime %s method %s: a built install needs a recipe", rt.ID(), m.ID)
				}
			default:
				return nil, fmt.Errorf("runtime %s method %s: kind required", rt.ID(), m.ID)
			}
		}
		if policy := rt.Policy(); policy != nil && policy.ContextParam != "" && !seen[policy.ContextParam] {
			return nil, fmt.Errorf("runtime %s: context param %s is not a param", rt.ID(), policy.ContextParam)
		}
		r.list = append(r.list, rt)
		r.byID[rt.ID()] = rt
	}
	sort.Slice(r.list, func(i, j int) bool { return r.list[i].ID() < r.list[j].ID() })
	return r, nil
}

// Checks that a solved param defaults to auto and says in words what auto resolves to, and that no other param claims a rule
func checkRule(p *v1.Param) error {
	if !p.GetSolved() {
		if p.GetRule() != "" {
			return fmt.Errorf("param %s: a rule belongs to a solved param", p.GetName())
		}
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(p.GetDefault()), Auto) {
		return fmt.Errorf("param %s: a solved param defaults to %s", p.GetName(), Auto)
	}
	if strings.TrimSpace(p.GetRule()) == "" {
		return fmt.Errorf("param %s: a solved param needs a rule saying what auto resolves to", p.GetName())
	}
	return nil
}

// The logical processors of every CPU device, as the probes counted them
func cpuThreads(h *v1.HostProfile) float64 {
	var n float64
	for _, d := range h.GetDevices() {
		if d.GetKind() != v1.DeviceKind_DEVICE_KIND_CPU {
			continue
		}
		if v, err := strconv.ParseFloat(d.GetFacts()["threads"], 64); err == nil {
			n += v
		}
	}
	return n
}

// The driver version the first device of a vendor reports, empty without one
func driverVersion(h *v1.HostProfile, vendor string) string {
	for _, d := range h.GetDevices() {
		if d.GetVendor() == vendor {
			if v := d.GetFacts()["driver_version"]; v != "" {
				return v
			}
		}
	}
	return ""
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

// Lists runtimes by id
func (r *Registry) List() []Runtime { return r.list }

// Returns one runtime by id
func (r *Registry) Get(id string) (Runtime, error) {
	rt, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownRuntime, id)
	}
	return rt, nil
}

// Returns the wire format a runtime's server speaks, OpenAI when the runtime is unknown
func (r *Registry) API(id string) v1.ApiFlavor {
	if r != nil {
		if rt, ok := r.byID[id]; ok && rt.API() != v1.ApiFlavor_API_FLAVOR_UNSPECIFIED {
			return rt.API()
		}
	}
	return v1.ApiFlavor_API_FLAVOR_OPENAI
}

// Every runtime nebu ships
func All() []Runtime {
	return []Runtime{LlamaCpp{}, VLLM{}, SGLang{}, NeMo{}}
}

// Whether the runtime accepts a format
func Accepts(rt Runtime, formatID string) bool {
	return slices.Contains(rt.Formats(), formatID)
}

// Whether the host meets the runtime's requirements, and which it does not
func Compatible(rt Runtime, profile *v1.HostProfile) (bool, []string) {
	unmet := rt.Unmet(profile)
	return len(unmet) == 0, unmet
}

// The ids of the recipes a runtime builds from, in order, each once
func RecipeIDs(rt Runtime) []string {
	var out []string
	for _, m := range rt.Methods() {
		if m.RecipeID != "" && !slices.Contains(out, m.RecipeID) {
			out = append(out, m.RecipeID)
		}
	}
	return out
}

// One install method by id
func MethodOf(rt Runtime, id string) (Method, error) {
	for _, m := range rt.Methods() {
		if m.ID == id {
			return m, nil
		}
	}
	return Method{}, fmt.Errorf("%w: %s has no install method %q", ErrParam, rt.ID(), id)
}

// The prebuilt rules whose conditions hold on the host, in order
func (m Method) HostRules(profile *v1.HostProfile) []PrebuiltRule {
	var out []PrebuiltRule
	for _, r := range m.Rules {
		if r.Applies(profile) {
			out = append(out, r)
		}
	}
	return out
}

// One prebuilt rule by id
func (m Method) Rule(id string) (PrebuiltRule, error) {
	for _, r := range m.Rules {
		if r.ID == id {
			return r, nil
		}
	}
	return PrebuiltRule{}, fmt.Errorf("%w: install method %s has no build %q", ErrParam, m.ID, id)
}

// A runtime as the API describes it
func Describe(rt Runtime) *v1.Runtime {
	out := &v1.Runtime{
		Id:           rt.ID(),
		Name:         rt.Name(),
		Description:  rt.Description(),
		Formats:      rt.Formats(),
		Api:          rt.API(),
		Requirements: rt.Requirements(),
		Params:       rt.Params(),
	}
	for _, m := range rt.Methods() {
		out.Methods = append(out.Methods, &v1.InstallMethod{Id: m.ID, Description: m.Description, Kind: m.Kind, Binaries: m.Binaries, Releases: m.Releases, RecipeId: m.RecipeID})
	}
	return out
}

// Builds status for API responses
func Status(rt Runtime, profile *v1.HostProfile) *v1.RuntimeStatus {
	ok, unmet := Compatible(rt, profile)
	return &v1.RuntimeStatus{Runtime: Describe(rt), Compatible: ok, Unmet: unmet}
}

// Resolves typed params from the runtime's defaults and the overrides given
func Resolve(rt Runtime, overrides map[string]string) (estimate.Params, error) {
	params := rt.Params()
	out := make(estimate.Params, len(params))
	byName := map[string]*v1.Param{}
	for _, p := range params {
		byName[p.GetName()] = p
		v, err := convert(p, p.GetDefault())
		if err != nil {
			return nil, err
		}
		out[p.GetName()] = v
	}
	for name, raw := range overrides {
		p, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("%w: unknown param %q for runtime %s", ErrParam, name, rt.ID())
		}
		v, err := convert(p, raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrParam, err)
		}
		out[name] = v
	}
	return out, nil
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

// Turns every set param into its flag: a solved param still at auto and an empty string emit nothing, a
// true flag stands alone, a flag ending in = joins its value, an env param sets its variable
func Flags(params []*v1.Param, values estimate.Params) (args []string, env map[string]string, emitted map[string]string) {
	env = map[string]string{}
	emitted = map[string]string{}
	for _, p := range params {
		value, ok := values[p.GetName()]
		if !ok {
			continue
		}
		var text string
		switch v := value.(type) {
		case nil:
			continue
		case string:
			if p.GetSolved() && v == Auto {
				continue
			}
			if v == "" {
				continue
			}
			text = v
		case bool:
			text = strconv.FormatBool(v)
		default:
			text = fmt.Sprint(v)
		}
		emitted[p.GetName()] = text
		if p.GetEnv() != "" {
			env[p.GetEnv()] = text
		}
		if p.GetFlag() == "" {
			continue
		}
		if b, isBool := value.(bool); isBool {
			if b {
				args = append(args, p.GetFlag())
			}
			continue
		}
		if strings.HasSuffix(p.GetFlag(), "=") {
			args = append(args, p.GetFlag()+text)
		} else {
			args = append(args, p.GetFlag(), text)
		}
	}
	return args, env, emitted
}

// The devices of a vendor the run is pinned to, joined by comma, by id or by the index the driver numbers them
func visible(devices []*v1.Device, vendor string, byIndex bool) string {
	var out []string
	for _, d := range devices {
		if vendor != "" && d.GetVendor() != vendor {
			continue
		}
		if vendor == "" && d.GetKind() != v1.DeviceKind_DEVICE_KIND_GPU {
			continue
		}
		if byIndex {
			if i := d.GetFacts()["index"]; i != "" {
				out = append(out, i)
			}
			continue
		}
		out = append(out, d.GetId())
	}
	return strings.Join(out, ",")
}

// Sets a variable when its value is not empty, so no slot means every device
func setEnv(env map[string]string, key, value string) {
	if value != "" {
		env[key] = value
	}
}

// Hides every accelerator from a process that keeps the model in host memory
func hideDevices(env map[string]string) {
	env["CUDA_VISIBLE_DEVICES"] = ""
	env["ROCR_VISIBLE_DEVICES"] = ""
}

// Refuses a launch in host memory for a runtime that runs in device memory alone
func deviceBound(in Launch, name string) error {
	if in.Placement == v1.Placement_PLACEMENT_HOST {
		return fmt.Errorf("%w: %s runs in device memory only, but the slot keeps the model in host memory", ErrParam, name)
	}
	return nil
}

// A byte count a log line prints as a number and a unit after a phrase, 5123.45 MiB
func bytesAfter(line, phrase string) (uint64, bool) {
	i := strings.Index(line, phrase)
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(line[i+len(phrase):])
	if len(fields) < 2 {
		return 0, false
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	unit := strings.ToLower(strings.TrimRight(fields[1], ",.;)"))
	mult := map[string]float64{"b": 1, "kb": 1 << 10, "kib": 1 << 10, "mb": 1 << 20, "mib": 1 << 20, "gb": 1 << 30, "gib": 1 << 30, "tb": 1 << 40, "tib": 1 << 40}[unit]
	if mult == 0 {
		return 0, false
	}
	return uint64(n * mult), true
}

// Measurements summed by key in first seen order, with the line each first came from
type measurements struct {
	byKey map[string]*v1.Measurement
	list  []*v1.Measurement
}

func (m *measurements) add(key string, bytes uint64, line string) {
	if m.byKey == nil {
		m.byKey = map[string]*v1.Measurement{}
	}
	ms, ok := m.byKey[key]
	if !ok {
		ms = &v1.Measurement{Key: key, Line: line}
		m.byKey[key] = ms
		m.list = append(m.list, ms)
	}
	ms.Bytes += bytes
}

// The first field of the output that reads as a dotted version
func versionField(output string) (string, bool) {
	for _, f := range strings.Fields(output) {
		f = strings.Trim(f, "(),")
		if f != "" && f[0] >= '0' && f[0] <= '9' && strings.Contains(f, ".") {
			return f, true
		}
	}
	return "", false
}

// A numeric param's bounds under the model and host, for a form to draw a slider with
func bounds(name string, min, max, step float64) *v1.ParamState {
	return &v1.ParamState{Name: name, Min: min, Max: max, Step: step}
}
