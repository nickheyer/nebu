// Package runtimes installs, launches, and monitors inference servers.
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

// Placeholder for parameters resolved by the planner.
const Auto = estimate.Auto

// Default shutdown grace period.
const DefaultStopGrace = 15 * time.Second

// Shared rule for automatic context length.
const contextRule = "the largest context that fits in memory, up to the length the model was trained for"

var (
	// Returned when a runtime id is not known
	ErrUnknownRuntime = errors.New("unknown runtime")
	// Returned when a param override is invalid
	ErrParam = errors.New("invalid param")
)

// Installed runtime path and metadata.
type Install struct {
	Path, Dir, Version string
}

// Launch inputs.
type Launch struct {
	Name   string
	Params estimate.Params
	// Stored paths by role, including weights_dir and prepared_dir.
	Artifacts map[string]string
	Host      string
	Port      int
	Install   Install
	// Assigned devices. Empty allows all devices.
	Devices []*v1.Device
	// Memory placement. Host placement disables accelerators.
	Placement  v1.Placement
	Descriptor *v1.Descriptor
	// Resolved memory plan, or nil if unsupported.
	Plan *v1.MemoryPlan
	// Other stored models available as components.
	Stored []*v1.StoredModel
	// Stored model and its pipeline files.
	Model *v1.StoredModel
}

// Resolved command and parameters.
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

// Release asset filename matcher.
type Asset struct {
	Prefix   string
	Contains string
	Suffix   string
}

func (a Asset) Matches(name string) bool {
	return strings.HasPrefix(name, a.Prefix) && strings.Contains(name, a.Contains) && strings.HasSuffix(name, a.Suffix)
}

// Description of the selected build.
func (a Asset) String() string { return a.Prefix + "*" + a.Contains + "*" + a.Suffix }

// Published build and host requirements.
type PrebuiltRule struct {
	ID      string
	Applies func(h *v1.HostProfile) bool
	Assets  []Asset
	Binary  string
}

// Runtime installation method.
type Method struct {
	ID          string
	Description string
	Kind        v1.InstallKind
	// Binaries looked for on PATH, for an adopted install
	Binaries []string
	// Release repository and available builds.
	Releases string
	Rules    []PrebuiltRule
	// Recipe ID for source builds.
	RecipeID string
}

// Command that extracts one runtime fact.
type Probe struct {
	Key string
	// Command path, defaulting to the installed runtime.
	Command func(in Install) string
	Args    []string
	Timeout time.Duration
	// Parses a fact from command output.
	Parse func(output string) (string, bool)
}

// Runtime installation, launch, probes, memory policy, and failure rules.
type Runtime interface {
	ID() string
	Name() string
	Description() string
	Formats() []string
	// Supported model kind.
	Kind() v1.ModelKind
	API() v1.ApiFlavor
	// Host requirements.
	Requirements() []string
	// Unmet host requirements.
	Unmet(h *v1.HostProfile) []string
	Methods() []Method
	Params() []*v1.Param
	Launch(in Launch) (*Command, error)
	// Whether a format requires preparation before launch.
	Prepares(formatID string) bool
	// Preparation command, or nil if unnecessary.
	Prepare(in Launch) (*Command, error)
	PrepareTimeout() time.Duration
	Health() Health
	StopGrace() time.Duration
	Policy() *estimate.Policy
	Probes() []Probe
	// Memory allocations parsed from logs, summed by key.
	Measure(lines []string) []*v1.Measurement
	Triage() []triage.Set
}

// Runtimes ordered by id
type Registry struct {
	list []Runtime
	byID map[string]Runtime
}

// Indexes runtimes and validates parameter definitions.
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

// Requires solved parameters to default to auto and define a rule. Unsolved parameters must not
// define rules.
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

// Total probed CPU threads.
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

// First reported driver version for the vendor, or empty.
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

// Validates numeric bounds and positive steps.
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

// Returns the runtime's bit by registry order, or zero if unknown.
func (r *Registry) Bit(id string) uint32 {
	for i, rt := range r.list {
		if rt.ID() == id {
			return 1 << uint(i)
		}
	}
	return 0
}

// Returns runtimes in the mask, ordered by ID.
func (r *Registry) Named(mask uint32) []Runtime {
	var out []Runtime
	for i, rt := range r.list {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, rt)
		}
	}
	return out
}

// Returns runtimes supporting the format and model kind, independent of host support.
func (r *Registry) Mask(formatID string, kind v1.ModelKind) uint32 {
	var mask uint32
	for i, rt := range r.list {
		if Accepts(rt, formatID, kind) {
			mask |= 1 << uint(i)
		}
	}
	return mask
}

// Returns runtimes supporting any listed format. Unknown model kinds return zero.
func (r *Registry) MaskOf(formatIDs []string, kind v1.ModelKind) uint32 {
	if kind == v1.ModelKind_MODEL_KIND_UNSPECIFIED {
		return 0
	}
	var mask uint32
	for _, id := range formatIDs {
		mask |= r.Mask(id, kind)
	}
	return mask
}

// Builds API status including runtime mask bits.
func (r *Registry) Status(rt Runtime, profile *v1.HostProfile) *v1.RuntimeStatus {
	st := Status(rt, profile)
	st.Runtime.Bit = r.Bit(rt.ID())
	return st
}

// Returns one runtime by id
func (r *Registry) Get(id string) (Runtime, error) {
	rt, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownRuntime, id)
	}
	return rt, nil
}

// Returns the server API flavor, defaulting to OpenAI for unknown runtimes.
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
	return []Runtime{LlamaCpp{}, VLLM{}, SGLang{}, NeMo{}, SDCpp{}}
}

// Matches the format and model kind. Standalone components cannot be served.
func Accepts(rt Runtime, formatID string, kind v1.ModelKind) bool {
	if !slices.Contains(rt.Formats(), formatID) {
		return false
	}
	if kind == v1.ModelKind_MODEL_KIND_UNSPECIFIED {
		kind = v1.ModelKind_MODEL_KIND_LANGUAGE
	}
	return kind == rt.Kind()
}

// Checks host requirements.
func Compatible(rt Runtime, profile *v1.HostProfile) (bool, []string) {
	unmet := rt.Unmet(profile)
	return len(unmet) == 0, unmet
}

// Returns unique build recipe IDs in order.
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

// Returns builds supported by the host, in rule order.
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
		Kind:         rt.Kind(),
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

// Merges parameter maps, with later values taking precedence.
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
	if len(p.GetChoices()) > 0 && p.GetPicks() == "" && !slices.Contains(p.GetChoices(), raw) {
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

// Converts parameters to flags or environment variables. Skips empty strings and unresolved auto
// values. True flags stand alone, and flags ending in = join their values.
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

// Returns assigned device IDs or driver indexes for the vendor, separated by commas.
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

// Sets nonempty device restrictions. Empty leaves all devices visible.
func setEnv(env map[string]string, key, value string) {
	if value != "" {
		env[key] = value
	}
}

// Disables accelerators for host memory placement.
func hideDevices(env map[string]string) {
	env["CUDA_VISIBLE_DEVICES"] = ""
	env["ROCR_VISIBLE_DEVICES"] = ""
}

// Rejects host placement for device-only runtimes.
func deviceBound(in Launch, name string) error {
	if in.Placement == v1.Placement_PLACEMENT_HOST {
		return fmt.Errorf("%w: %s runs in device memory only, but the slot keeps the model in host memory", ErrParam, name)
	}
	return nil
}

// Parses a number and unit following a log phrase, such as 5123.45 MiB.
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

// Measurements summed by key, preserving the first matching line.
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

// Returns the first dotted version in command output.
func versionField(output string) (string, bool) {
	for _, f := range strings.Fields(output) {
		f = strings.Trim(f, "(),")
		if f != "" && f[0] >= '0' && f[0] <= '9' && strings.Contains(f, ".") {
			return f, true
		}
	}
	return "", false
}

// Numeric parameter bounds for the current model and host.
func bounds(name string, min, max, step float64) *v1.ParamState {
	return &v1.ParamState{Name: name, Min: min, Max: max, Step: step}
}
