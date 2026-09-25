// Package instances runs, supervises, records, recovers, and routes models.
package instances

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/calibrate"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/launch"
	"github.com/nickheyer/nebu/pkg/proc"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/text"
	"github.com/nickheyer/nebu/pkg/triage"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	kindRun      = "run"
	historyMax   = 100
	bindHost     = "127.0.0.1"
	closeTimeout = 30 * time.Second
	probeTimeout = 2 * time.Second
	// Combined timeout for both chat template probes.
	templateTimeout = 2 * time.Minute
)

var (
	// Returned when an instance id or name is not known
	ErrUnknownInstance = errors.New("unknown instance")
	errStopped         = errors.New("stopped before ready")
)

// Narrows a run to a slot's devices, budget, and defaults
type Reservation struct {
	SlotID      string
	Name        string
	DeviceIDs   []string
	MemoryBytes uint64
	Placement   v1.Placement
	RuntimeID   string
	Params      map[string]string
	InstanceID  string
	// The formation serving the slot, when one does
	FormationID   string
	FormationName string
}

func (r *Reservation) placement() v1.Placement {
	if r == nil {
		return v1.Placement_PLACEMENT_UNSPECIFIED
	}
	return r.Placement
}

type swapKey struct{}

// Marks a context as a swap, allowed beside an occupant
func WithSwap(ctx context.Context) context.Context { return context.WithValue(ctx, swapKey{}, true) }

func fromSwap(ctx context.Context) bool { v, _ := ctx.Value(swapKey{}).(bool); return v }

// Whether the context marks a swap, so a replacement may launch beside the occupant it replaces
func FromSwap(ctx context.Context) bool { return fromSwap(ctx) }

// Slot manager interface for bound instances.
type Slots interface {
	Reservation(ctx context.Context, slotID string) (*Reservation, error)
	// Called after every state change, outside the instance lock
	OnInstance(*v1.Instance)
}

// What a seat's node asks about the formation a seat serves: for a stage adopted after a restart
// while a head holds it, the conductor's record is the evidence that the head is ready on it
type Formations interface {
	// Waits until a record of the formation's conductor written after since says the head is ready,
	// and returns why not when the record says otherwise or ctx ends first
	HeadReady(ctx context.Context, formationID string, since time.Time) error
}

// An adopted stage a head holds waits this long for the conductor's record
const adoptConfirmWindow = 2 * time.Minute

// Manages daemon instances.
type Manager struct {
	DB          *db.DB
	Dir         string
	Store       *store.Store
	Runtimes    *runtimes.Registry
	Installs    *installs.Manager
	Inspector   *inspect.Inspector
	Launcher    launch.Launcher
	Tasks       *tasks.Manager
	Host        *host.Prober
	Calibration *calibrate.Table
	Routes      *gateway.Table
	Events      *events.Bus
	// Set by the daemon once slots exist, nil until then
	Slots Slots
	// Set by the daemon once the conductor exists, nil until then
	Formations Formations
	Log        *slog.Logger

	mu   sync.Mutex
	list []*instance
}

type instance struct {
	mgr      *Manager
	mu       sync.Mutex
	rec      *v1.Instance
	rt       runtimes.Runtime
	proc     launch.Handle
	log      *launch.Log
	exited   chan struct{}
	exitOnce sync.Once
	// The guard listener in front of a seat, nil for a solo instance or an exposed seat
	guard *forwarder
}

func (m *Manager) newInstance(rec *v1.Instance, rt runtimes.Runtime) *instance {
	in := &instance{mgr: m, rec: rec, rt: rt, exited: make(chan struct{})}
	if Terminal(rec.GetState()) {
		in.exitOnce.Do(func() { close(in.exited) })
	}
	return in
}

func (in *instance) snapshot() *v1.Instance {
	in.mu.Lock()
	defer in.mu.Unlock()
	return proto.Clone(in.rec).(*v1.Instance)
}

// Applies and stores a state change, then wakes waiters and publishes it.
func (in *instance) update(fn func(*v1.Instance)) {
	in.mu.Lock()
	before := in.rec.GetState()
	fn(in.rec)
	if err := in.mgr.DB.PutInstance(context.Background(), in.rec); err != nil {
		in.mgr.Log.Warn("instance record write failed", "id", in.rec.GetId(), "err", err)
	}
	rec := proto.Clone(in.rec).(*v1.Instance)
	in.mu.Unlock()
	in.mgr.changed(rec, before)
	// Publish exits before releasing stop waiters.
	if Terminal(rec.GetState()) {
		in.mu.Lock()
		g := in.guard
		in.guard = nil
		in.mu.Unlock()
		if g != nil {
			g.Close()
		}
		in.exitOnce.Do(func() { close(in.exited) })
	}
}

// Publishes instance changes and updates routes. Slots manage their own routes.
func (m *Manager) changed(rec *v1.Instance, before v1.InstanceState) {
	slotted := rec.GetSlotId() != ""
	switch {
	case m.Routes == nil:
	case Terminal(rec.GetState()) && !Terminal(before):
		m.Routes.RemoveInstance(rec.GetId())
	case rec.GetSeat() != nil:
		// The conductor routes formations.
	case slotted:
	case rec.GetState() == v1.InstanceState_INSTANCE_STATE_READY:
		m.Routes.Serve(rec.GetName(), rec, m.Runtimes.API(rec.GetRuntimeId()), "", nil, nil)
	case rec.GetState() == v1.InstanceState_INSTANCE_STATE_DRAINING && before != v1.InstanceState_INSTANCE_STATE_DRAINING:
		m.Routes.Drain(rec.GetId())
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_INSTANCE, v1.EventAction_EVENT_ACTION_UPDATED, rec.GetId(), rec)
	if m.Slots != nil {
		m.Slots.OnInstance(rec)
	}
}

func (in *instance) grace() time.Duration {
	if in.rt != nil && in.rt.StopGrace() > 0 {
		return in.rt.StopGrace()
	}
	return runtimes.DefaultStopGrace
}

func (in *instance) stopRequested() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.rec.GetState() == v1.InstanceState_INSTANCE_STATE_STOPPING || in.rec.GetState() == v1.InstanceState_INSTANCE_STATE_STOPPED
}

func (in *instance) handle() launch.Handle {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.proc
}

func (in *instance) attach(proc launch.Handle) {
	in.mu.Lock()
	in.proc, in.log = proc, proc.Log()
	in.mu.Unlock()
}

const (
	defaultPrepareTimeout = time.Hour
	// Preparation log capacity, matching the launcher ring.
	logCapacity = 5000
)

// Resolved launch inputs.
type prepared struct {
	req        *v1.RunRequest
	stored     *v1.StoredModel
	rt         runtimes.Runtime
	install    *v1.Install
	name       string
	descriptor *v1.Descriptor
	profile    *v1.HostProfile
	planned    *v1.HostProfile
	res        *Reservation
	params     estimate.Params
	plan       *v1.MemoryPlan
	// Other stored models available as companion parts.
	companions []*v1.StoredModel
	// The seat block and its role, for a seat of a formation
	seat *v1.SeatSpec
	role runtimes.Role
}

// Plans a run without launching it
func (m *Manager) Plan(ctx context.Context, req *v1.RunRequest) (*v1.MemoryPlan, error) {
	p, err := m.prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	if p.plan == nil {
		return &v1.MemoryPlan{Verdict: v1.FitVerdict_FIT_VERDICT_FITS, Detail: "runtime has no estimate policy"}, nil
	}
	return p.plan, nil
}

// Plans and launches a stored model, returning record and task
func (m *Manager) Run(ctx context.Context, req *v1.RunRequest) (*v1.Instance, *v1.Task, error) {
	p, err := m.prepare(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if p.res != nil && p.res.InstanceID != "" && !fromSwap(ctx) {
		if cur, err := m.Get(p.res.InstanceID); err == nil && !Terminal(cur.GetState()) {
			return nil, nil, fmt.Errorf("%w: slot %s serves %s, swap instead", runtimes.ErrParam, p.res.Name, cur.GetName())
		}
	}
	if p.res != nil && p.res.FormationID != "" && !fromSwap(ctx) {
		return nil, nil, fmt.Errorf("%w: slot %s serves formation %s, swap instead", runtimes.ErrParam, p.res.Name, p.res.FormationName)
	}
	if p.seat == nil && p.plan != nil && p.plan.GetVerdict() == v1.FitVerdict_FIT_VERDICT_NO && !p.req.GetForce() {
		return nil, nil, fmt.Errorf("%w: %s does not fit, %s. Pass force to run anyway", runtimes.ErrParam, p.name, p.plan.GetDetail())
	}
	return m.launch(ctx, p)
}

// Resolves model, runtime, install, reservation, and plan
func (m *Manager) prepare(ctx context.Context, req *v1.RunRequest) (*prepared, error) {
	if req.GetSeat() != nil {
		return m.prepareSeat(ctx, req)
	}
	req = proto.Clone(req).(*v1.RunRequest)
	stored, err := m.Store.ReadManifest(req.GetSourceId(), req.GetRepo(), req.GetGroup())
	if err != nil {
		return nil, err
	}
	var res *Reservation
	if req.GetSlotId() != "" {
		if res, err = m.reservation(ctx, req.GetSlotId()); err != nil {
			return nil, err
		}
		req.SlotId = res.SlotID
		if req.RuntimeId == "" {
			req.RuntimeId = res.RuntimeID
		}
		if req.Name == "" {
			req.Name = res.Name
		}
	}
	descriptor, err := m.describe(ctx, stored)
	if err != nil {
		return nil, err
	}
	profile, err := m.Host.Profile(ctx, true)
	if err != nil {
		return nil, err
	}
	// Use the slot's runtime or the first compatible runtime.
	var rt runtimes.Runtime
	if req.GetRuntimeId() == "" {
		rt, err = m.Inspector.DefaultRuntime(profile, stored.GetFormatId(), descriptor.GetKind())
	} else {
		rt, err = m.Runtimes.Get(req.GetRuntimeId())
	}
	if err != nil {
		return nil, err
	}
	req.RuntimeId = rt.ID()
	if !runtimes.Accepts(rt, stored.GetFormatId(), descriptor.GetKind()) {
		if descriptor.GetKind() == v1.ModelKind_MODEL_KIND_COMPONENT {
			return nil, fmt.Errorf("%w: %s is a part loaded beside a diffusion model, not one served on its own", runtimes.ErrParam, stored.GetGroup())
		}
		return nil, fmt.Errorf("%w: runtime %s does not serve %s %s models", runtimes.ErrParam, rt.ID(), stored.GetFormatId(), strings.ToLower(strings.TrimPrefix(descriptor.GetKind().String(), "MODEL_KIND_")))
	}
	var install *v1.Install
	if req.GetInstallId() != "" {
		install, err = m.Installs.Get(ctx, req.GetInstallId())
	} else {
		install, err = m.Installs.Default(ctx, rt.ID())
	}
	if err != nil {
		return nil, err
	}
	name := req.GetName()
	if name == "" {
		name = path.Base(stored.GetRepo()) + ":" + stored.GetGroup()
	}
	if m.conflict(name, req.GetSlotId()) {
		return nil, fmt.Errorf("%w: instance %q is already running", runtimes.ErrParam, name)
	}
	if req.GetSlotId() == "" && m.Routes != nil {
		if r, ok := m.Routes.Lookup(name); ok && r.GetSlotId() != "" {
			return nil, fmt.Errorf("%w: %q is a slot, run with the slot or pick another name", runtimes.ErrParam, name)
		}
	}
	planProfile := profile
	if res != nil {
		planProfile = Constrain(profile, res.DeviceIDs, res.MemoryBytes, res.Placement)
	}
	var slotParams map[string]string
	if res != nil {
		slotParams = res.Params
	}
	companions, err := m.Inspector.Companions(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup())
	if err != nil {
		return nil, err
	}
	// Request params override slot defaults. Stored groups named by path params resolve to their files.
	layered, err := runtimes.ResolveStore(rt, runtimes.Merge(slotParams, req.GetParams()), append(append([]*v1.StoredModel{}, companions...), stored))
	if err != nil {
		return nil, err
	}
	params, err := runtimes.Resolve(rt, layered)
	if err != nil {
		return nil, err
	}
	p := &prepared{req: req, stored: stored, rt: rt, install: install, name: name, descriptor: descriptor, profile: profile, planned: planProfile, res: res, params: params}
	p.companions = companions
	if rt.Policy() != nil {
		if p.plan, err = m.Inspector.Plan(rt, descriptor, planProfile, layered, true, res.placement(), stored.GetRepo(), stored, companions, req.GetForce()); err != nil {
			return nil, err
		}
		for k, v := range p.plan.GetParams() {
			if params.IsAuto(k) {
				params[k] = v
			}
		}
	}
	return p, nil
}

// Renders and launches a prepared run
func (m *Manager) launch(ctx context.Context, p *prepared) (*v1.Instance, *v1.Task, error) {
	if p.seat != nil {
		return m.launchSeat(ctx, p)
	}
	req, stored, rt, install, name, descriptor, profile, params, plan := p.req, p.stored, p.rt, p.install, p.name, p.descriptor, p.profile, p.params, p.plan
	port, err := freePort()
	if err != nil {
		return nil, nil, err
	}
	// Update last-used time only on launch. Hold the model lock until the instance
	// is registered so eviction can see and protect it.
	key := store.Key(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup())
	unlock := m.Store.Lock(key)
	defer unlock()
	touched, err := m.Store.Touch(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup())
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s is no longer stored, pull it again: %w", stored.GetRepo(), stored.GetGroup(), err)
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_MODEL, v1.EventAction_EVENT_ACTION_UPDATED, key, touched)
	artifacts := m.artifacts(stored, rt)
	input := runtimes.Launch{
		Name:          name,
		Params:        params,
		Artifacts:     artifacts,
		Host:          bindHost,
		Port:          port,
		Install:       runtimes.Install{Path: install.GetPath(), Dir: install.GetDir(), Version: install.GetVersion()},
		Devices:       slotDevices(p.planned, p.res),
		Placement:     p.res.placement(),
		Descriptor:    descriptor,
		Plan:          plan,
		Stored:        p.companions,
		Model:         stored,
		Force:         req.GetForce(),
		InstallRecord: install,
	}
	var prep *runtimes.Command
	if rt.Prepares(stored.GetFormatId()) {
		if artifacts["prepared_dir"] == "" {
			return nil, nil, fmt.Errorf("%s needs a prepared tree for %s and the store has none", rt.ID(), stored.GetGroup())
		}
		if prep, err = rt.Prepare(input); err != nil {
			return nil, nil, err
		}
		launchArtifacts := make(map[string]string, len(artifacts))
		for k, v := range artifacts {
			launchArtifacts[k] = v
		}
		launchArtifacts["weights_dir"] = artifacts["prepared_dir"]
		input.Artifacts = launchArtifacts
	}
	rendered, err := rt.Launch(input)
	if err != nil {
		return nil, nil, err
	}
	in := m.newInstance(&v1.Instance{
		Id:             db.NewID(),
		Name:           name,
		SourceId:       stored.GetSourceId(),
		Repo:           stored.GetRepo(),
		Group:          stored.GetGroup(),
		RuntimeId:      rt.ID(),
		InstallId:      install.GetId(),
		Params:         rendered.Params,
		Command:        append([]string{rendered.Command}, rendered.Args...),
		Endpoint:       fmt.Sprintf("http://%s:%d", bindHost, port),
		State:          v1.InstanceState_INSTANCE_STATE_STARTING,
		CreatedAt:      timestamppb.Now(),
		Plan:           plan,
		Request:        proto.Clone(req).(*v1.RunRequest),
		DesiredRunning: true,
		SlotId:         req.GetSlotId(),
	}, rt)
	m.mu.Lock()
	if m.conflictLocked(name, req.GetSlotId()) {
		m.mu.Unlock()
		return nil, nil, fmt.Errorf("%w: instance %q is already running", runtimes.ErrParam, name)
	}
	m.list = append(m.list, in)
	m.pruneLocked()
	m.mu.Unlock()
	m.Events.Publish(v1.EventKind_EVENT_KIND_INSTANCE, v1.EventAction_EVENT_ACTION_CREATED, in.rec.GetId(), in.snapshot())
	prepDir := artifacts["prepared_dir"]
	prepTimeout := rt.PrepareTimeout()
	task := m.Tasks.Start(kindRun, "run "+name, map[string]string{"instance": in.rec.GetId(), "name": name}, func(ctx context.Context, h *tasks.Handle) error {
		if prep != nil {
			if err := m.runPrepare(ctx, h, in, prep, install.GetDir(), prepDir, prepTimeout, req.GetForce()); err != nil {
				return err
			}
		}
		return m.start(ctx, h, in, rendered, install, descriptor, profile)
	})
	in.update(func(r *v1.Instance) { r.TaskId = task.GetId() })
	return in.snapshot(), task, nil
}

func (m *Manager) runPrepare(ctx context.Context, h *tasks.Handle, in *instance, prep *runtimes.Command, dir, prepDir string, timeout time.Duration, force bool) error {
	marker := filepath.Join(prepDir, ".prepared")
	if _, err := os.Stat(marker); err == nil && !force {
		h.Logf("prepared earlier at %s", prepDir)
		return nil
	}
	if err := os.RemoveAll(prepDir); err != nil {
		return err
	}
	if err := os.MkdirAll(prepDir, 0o755); err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = defaultPrepareTimeout
	}
	h.Progress(0, 0, "preparing")
	h.Logf("prepare: %s %s", prep.Command, strings.Join(prep.Args, " "))
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(pctx, prep.Command, prep.Args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range prep.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	logFile, err := os.OpenFile(m.logPath(in.snapshot().GetId()), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	// Use preparation output as the instance log until the runtime starts.
	log := launch.NewLog(logCapacity)
	in.mu.Lock()
	in.log = log
	in.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintln(logFile, line)
			log.Write(line)
			h.Logf("%s", line)
		}
	}()
	// Cancel the entire converter process tree, including workers.
	err = proc.Run(cmd)
	pw.Close()
	<-done
	log.Close()
	if err != nil {
		os.RemoveAll(prepDir)
		err = fmt.Errorf("prepare: %w", err)
		for _, hit := range m.fail(in, err) {
			h.Logf("triage %s: %s", hit.GetId(), hit.GetSummary())
		}
		return err
	}
	return os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

func (m *Manager) start(ctx context.Context, h *tasks.Handle, in *instance, rendered *runtimes.Command, install *v1.Install, d *v1.Descriptor, before *v1.HostProfile) error {
	rec := in.snapshot()
	h.Logf("command: %s", strings.Join(rec.GetCommand(), " "))
	h.Progress(0, 0, "launching")
	proc, err := m.Launcher.Launch(ctx, launch.Spec{Command: rendered.Command, Args: rendered.Args, Env: rendered.Env, Dir: install.GetDir(), LogPath: m.logPath(rec.GetId())})
	if err != nil {
		in.update(func(r *v1.Instance) {
			if Terminal(r.State) {
				return
			}
			r.State = v1.InstanceState_INSTANCE_STATE_FAILED
			r.Error = err.Error()
			r.StoppedAt = timestamppb.Now()
		})
		return err
	}
	in.attach(proc)
	var stopping bool
	in.update(func(r *v1.Instance) {
		r.Pid = int32(proc.Pid())
		stopping = r.State == v1.InstanceState_INSTANCE_STATE_STOPPING
	})
	go m.supervise(in)
	if stopping {
		h.Logf("stop requested before the runtime was ready")
		proc.Stop(in.grace())
		return errStopped
	}
	if err := m.waitReady(ctx, h, in, proc); err != nil {
		return err
	}
	proc.Sync()
	measurements := in.rt.Measure(proc.Log().Tail(0))
	if after, perr := m.Host.Profile(ctx, true); perr == nil {
		used := deviceFree(before) - deviceFree(after)
		if used > 0 {
			measurements = append(measurements, &v1.Measurement{Key: estimate.DeviceUsedKey, Bytes: uint64(used)})
			if plan := rec.GetPlan(); plan != nil {
				base := max(float64(estimate.PlannedDevice(plan))-plan.GetOverheadDelta(), 0)
				if cerr := m.Calibration.Record(ctx, in.rt.ID(), d.GetArchitecture(), uint64(used), uint64(base)); cerr != nil {
					m.Log.Warn("calibration write failed", "err", cerr)
				}
			}
		}
	}
	m.ready(h, in, measurements, m.probeTemplate(ctx, h.Logf, in))
	return nil
}

// Probes chat template support and logs gateway system message handling.
func (m *Manager) probeTemplate(ctx context.Context, logf func(string, ...any), in *instance) *v1.TemplateProbe {
	rec := in.snapshot()
	// Probe diffusion capabilities and update its routes.
	if m.Runtimes.API(rec.GetRuntimeId()) == v1.ApiFlavor_API_FLAVOR_SDCPP {
		m.probeModes(ctx, logf, in)
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, templateTimeout)
	defer cancel()
	probe := gateway.ProbeTemplate(ctx, &http.Client{}, rec.GetEndpoint(), m.Runtimes.API(rec.GetRuntimeId()), rec.GetName())
	switch {
	case probe.GetError() != "":
		logf("chat template probe: %s. The gateway preserves system messages", probe.GetError())
	case probe.GetLateSystem():
		logf("chat template renders a system message after the first")
	default:
		logf("later system message rejected: %s. The gateway merges system messages unless the route overrides it", probe.GetRefusal())
	}
	return probe
}

// Reads diffusion capabilities for gateway image and video routing.
func (m *Manager) probeModes(ctx context.Context, logf func(string, ...any), in *instance) {
	rec := in.snapshot()
	ctx, cancel := context.WithTimeout(ctx, templateTimeout)
	defer cancel()
	modes, err := gateway.Capabilities(ctx, &http.Client{}, rec.GetEndpoint())
	if err != nil {
		logf("capabilities probe: %s. Route capabilities are unknown", err)
		return
	}
	logf("generates %s", strings.Join(modes, ", "))
	if m.Routes != nil {
		m.Routes.SetModes(rec.GetId(), modes)
	}
}

// Polls health, recording triage and stopping the process on failure
func (m *Manager) waitReady(ctx context.Context, h *tasks.Handle, in *instance, proc launch.Handle) error {
	health := in.rt.Health()
	url := in.snapshot().GetEndpoint() + health.Path
	h.Message("waiting for " + url)
	err := launch.WaitHealthy(ctx, proc, url, health.Interval, health.Timeout)
	if err == nil {
		return nil
	}
	if in.stopRequested() {
		h.Logf("stop requested before the runtime was ready")
		proc.Stop(in.grace())
		return errStopped
	}
	hits := m.fail(in, err)
	proc.Stop(in.grace())
	for _, hit := range hits {
		h.Logf("triage %s: %s. %s", hit.GetId(), hit.GetSummary(), hit.GetHint())
	}
	if len(hits) > 0 {
		return fmt.Errorf("%s: %s", hits[0].GetSummary(), hits[0].GetHint())
	}
	return err
}

// Marks active instances ready and records measurements and template probe results.
func (m *Manager) ready(h *tasks.Handle, in *instance, measurements []*v1.Measurement, probe *v1.TemplateProbe) {
	var rec *v1.Instance
	in.update(func(r *v1.Instance) {
		if r.State == v1.InstanceState_INSTANCE_STATE_STARTING {
			r.State = v1.InstanceState_INSTANCE_STATE_READY
			r.ReadyAt = timestamppb.Now()
		}
		r.Measurements = mergeMeasurements(r.Measurements, measurements)
		r.Template = probe
		rec = proto.Clone(r).(*v1.Instance)
	})
	for _, ms := range measurements {
		h.Logf("%s %s", ms.GetKey(), estimate.Human(ms.GetBytes()))
	}
	h.Progress(1, 1, "ready at "+rec.GetEndpoint())
	h.Logf("ready %s at %s", rec.GetName(), rec.GetEndpoint())
}

// Merges measurements by key, preserving old-only keys.
func mergeMeasurements(old, fresh []*v1.Measurement) []*v1.Measurement {
	if len(fresh) == 0 {
		return old
	}
	seen := map[string]bool{}
	for _, m := range fresh {
		seen[m.GetKey()] = true
	}
	out := append([]*v1.Measurement(nil), fresh...)
	for _, m := range old {
		if !seen[m.GetKey()] {
			out = append(out, m)
		}
	}
	return out
}

// Marks an instance failed unless terminal, returning triage hits
func (m *Manager) fail(in *instance, err error) []*v1.TriageHit {
	var hits []*v1.TriageHit
	in.mu.Lock()
	log, proc := in.log, in.proc
	in.mu.Unlock()
	if proc != nil {
		proc.Sync()
	}
	if log != nil && in.rt != nil {
		hits = triage.Scan(in.rt.Triage(), log.Tail(0))
	}
	in.update(func(r *v1.Instance) {
		if Terminal(r.State) || r.State == v1.InstanceState_INSTANCE_STATE_STOPPING {
			hits = r.Triage
			return
		}
		r.State = v1.InstanceState_INSTANCE_STATE_FAILED
		r.Error = err.Error()
		r.Triage = hits
		r.StoppedAt = timestamppb.Now()
	})
	return hits
}

func (m *Manager) supervise(in *instance) {
	proc := in.handle()
	<-proc.Done()
	exitErr := proc.Err()
	in.mu.Lock()
	state := in.rec.State
	in.mu.Unlock()
	switch state {
	case v1.InstanceState_INSTANCE_STATE_STOPPING, v1.InstanceState_INSTANCE_STATE_DRAINING:
		in.update(func(r *v1.Instance) {
			r.State = v1.InstanceState_INSTANCE_STATE_STOPPED
			r.StoppedAt = timestamppb.Now()
		})
	case v1.InstanceState_INSTANCE_STATE_READY, v1.InstanceState_INSTANCE_STATE_STARTING:
		detail := "exited"
		if exitErr != nil {
			detail = "exited: " + exitErr.Error()
		}
		hits := m.fail(in, errors.New(detail))
		m.Log.Warn("instance exited", "name", in.rec.GetName(), "err", exitErr, "triage", len(hits))
	}
}

// Lists instances newest first
func (m *Manager) List(runningOnly bool) []*v1.Instance {
	m.mu.Lock()
	list := append([]*instance(nil), m.list...)
	m.mu.Unlock()
	var out []*v1.Instance
	for i := len(list) - 1; i >= 0; i-- {
		rec := list[i].snapshot()
		if runningOnly && Terminal(rec.GetState()) {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// Returns one instance by id or name, preferring live ones
func (m *Manager) Get(id string) (*v1.Instance, error) {
	in, err := m.find(id)
	if err != nil {
		return nil, err
	}
	return in.snapshot(), nil
}

func (m *Manager) find(id string) (*instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var match *instance
	for i := len(m.list) - 1; i >= 0; i-- {
		in := m.list[i]
		rec := in.snapshot()
		if rec.GetId() != id && rec.GetName() != id {
			continue
		}
		if !Terminal(rec.GetState()) {
			return in, nil
		}
		if match == nil {
			match = in
		}
	}
	if match == nil {
		return nil, fmt.Errorf("%w %q", ErrUnknownInstance, id)
	}
	return match, nil
}

func (m *Manager) live(name string) *instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.liveLocked(name)
}

func (m *Manager) liveLocked(name string) *instance {
	for _, in := range m.list {
		rec := in.snapshot()
		if rec.GetName() == name && !Terminal(rec.GetState()) {
			return in
		}
	}
	return nil
}

// Reports a live instance with the name outside the slot
func (m *Manager) conflict(name, slotID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conflictLocked(name, slotID)
}

func (m *Manager) conflictLocked(name, slotID string) bool {
	for _, in := range m.list {
		rec := in.snapshot()
		if rec.GetName() != name || Terminal(rec.GetState()) {
			continue
		}
		if slotID == "" || rec.GetSlotId() != slotID {
			return true
		}
	}
	return false
}

// Returns the slot reservation or an error if slots are not initialized.
func (m *Manager) reservation(ctx context.Context, slotID string) (*Reservation, error) {
	if m.Slots == nil {
		return nil, fmt.Errorf("%w: slots are not available", runtimes.ErrParam)
	}
	return m.Slots.Reservation(ctx, slotID)
}

// Narrows a profile to a slot for planning, implementing the inspector's constrainer
func (m *Manager) Constrain(ctx context.Context, slotID string, profile *v1.HostProfile) (*inspect.Constraint, error) {
	res, err := m.reservation(ctx, slotID)
	if err != nil {
		return nil, err
	}
	return &inspect.Constraint{Profile: Constrain(profile, res.DeviceIDs, res.MemoryBytes, res.Placement), RuntimeID: res.RuntimeID, Params: res.Params, Placement: res.Placement}, nil
}

// Relaunch wanted, nonfailed instances outside slots. Slots manage their own recovery.
func relaunches(rec *v1.Instance) bool {
	return rec.GetDesiredRunning() && rec.GetState() != v1.InstanceState_INSTANCE_STATE_FAILED && rec.GetSlotId() == "" && rec.GetSeat() == nil
}

// Lists live instances bound to a slot, newest first
func (m *Manager) InSlot(slotID string) []*v1.Instance {
	var out []*v1.Instance
	for _, rec := range m.List(true) {
		if rec.GetSlotId() == slotID {
			out = append(out, rec)
		}
	}
	return out
}

// Stops taking new requests and waits for in flight ones
func (m *Manager) Drain(ctx context.Context, id string, limit time.Duration) (*v1.Instance, error) {
	in, err := m.find(id)
	if err != nil {
		return nil, err
	}
	var draining bool
	in.update(func(r *v1.Instance) {
		if r.State == v1.InstanceState_INSTANCE_STATE_READY {
			r.State = v1.InstanceState_INSTANCE_STATE_DRAINING
			draining = true
		}
	})
	if draining && m.Routes != nil {
		m.Routes.Drain(in.rec.GetId())
		m.Routes.WaitDrained(ctx, in.rec.GetId(), limit)
	}
	return in.snapshot(), nil
}

// Stops an instance by request, clearing relaunch intent
func (m *Manager) Stop(ctx context.Context, id string) (*v1.Instance, error) {
	in, err := m.find(id)
	if err != nil {
		return nil, err
	}
	return m.stop(ctx, in, true)
}

func (m *Manager) stop(ctx context.Context, in *instance, byRequest bool) (*v1.Instance, error) {
	var wasTerminal bool
	in.update(func(r *v1.Instance) {
		if byRequest {
			r.DesiredRunning = false
		}
		wasTerminal = Terminal(r.State)
		if !wasTerminal {
			r.State = v1.InstanceState_INSTANCE_STATE_STOPPING
		}
	})
	if wasTerminal {
		return in.snapshot(), nil
	}
	if proc := in.handle(); proc != nil {
		proc.Stop(in.grace())
	}
	select {
	case <-in.exited:
	case <-ctx.Done():
		return in.snapshot(), ctx.Err()
	}
	return in.snapshot(), nil
}

// Streams runtime output, following until exit when asked
func (m *Manager) Logs(ctx context.Context, id string, follow bool, tail int, send func([]string) error) error {
	in, err := m.find(id)
	if err != nil {
		return err
	}
	log := m.logOf(in)
	if log == nil {
		return nil
	}
	if !follow {
		lines := log.Tail(tail)
		if len(lines) == 0 {
			return nil
		}
		return send(lines)
	}
	return log.Follow(ctx, tail, send)
}

// Returns the live log or loads the saved output file
func (m *Manager) logOf(in *instance) *launch.Log {
	in.mu.Lock()
	log, proc := in.log, in.proc
	id := in.rec.GetId()
	in.mu.Unlock()
	if log != nil {
		if proc != nil {
			proc.Sync()
		}
		return log
	}
	lines, err := launch.ReadTail(m.logPath(id), 0)
	if err != nil {
		return nil
	}
	saved := launch.NewLog(max(len(lines), 1))
	for _, line := range lines {
		saved.Write(line)
	}
	saved.Close()
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.log == nil {
		in.log = saved
	}
	return in.log
}

// Stops every live instance for shutdown, keeping their relaunch intent
func (m *Manager) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, rec := range m.List(true) {
		in, err := m.find(rec.GetId())
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(in *instance) {
			defer wg.Done()
			m.stop(ctx, in, false)
		}(in)
	}
	wg.Wait()
}

// Loads records, adopts live runtimes, marks exited ones, and relaunches wanted instances.
func (m *Manager) Recover(ctx context.Context) error {
	loaded, err := m.load(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.list = loaded
	m.pruneLocked()
	m.mu.Unlock()
	var wg sync.WaitGroup
	wanted := map[string]*instance{}
	for _, in := range loaded {
		rec := in.snapshot()
		if !Terminal(rec.GetState()) {
			alive := proc.Running(int(rec.GetPid()), rec.GetCommand())
			switch {
			case alive && in.rt != nil && rec.GetDesiredRunning():
				m.adopt(ctx, in)
				continue
			case alive && in.rt == nil:
				m.Log.Warn("stopping instance whose runtime manifest is gone", "name", rec.GetName(), "runtime", rec.GetRuntimeId(), "pid", rec.GetPid())
				fallthrough
			case alive:
				if in.rt != nil {
					m.Log.Info("finishing the stop a previous daemon began", "name", rec.GetName(), "pid", rec.GetPid())
				}
				wg.Add(1)
				go func(pid int, grace time.Duration) {
					defer wg.Done()
					proc.Terminate(pid, grace)
				}(int(rec.GetPid()), in.grace())
			}
			in.update(func(r *v1.Instance) {
				r.State = v1.InstanceState_INSTANCE_STATE_STOPPED
				r.Error = db.RestartNote
				r.StoppedAt = timestamppb.Now()
			})
		}
		if relaunches(rec) {
			wanted[rec.GetName()] = in
		}
	}
	wg.Wait()
	var relaunch []*instance
	for name, in := range wanted {
		if m.live(name) == nil {
			relaunch = append(relaunch, in)
		}
	}
	sort.Slice(relaunch, func(i, j int) bool {
		return relaunch[i].snapshot().GetCreatedAt().AsTime().Before(relaunch[j].snapshot().GetCreatedAt().AsTime())
	})
	if len(relaunch) > 0 {
		go m.relaunch(ctx, relaunch)
	}
	return nil
}

// Adopts a runtime left running, routing it once healthy
func (m *Manager) adopt(ctx context.Context, in *instance) {
	rec := in.snapshot()
	proc := launch.Adopt(int(rec.GetPid()), m.logPath(rec.GetId()))
	in.attach(proc)
	go m.supervise(in)
	if rec.GetSeat() != nil {
		m.adoptSeat(ctx, in, proc)
		return
	}
	url := rec.GetEndpoint() + in.rt.Health().Path
	if rec.GetState() == v1.InstanceState_INSTANCE_STATE_READY && launch.Healthy(&http.Client{Timeout: probeTimeout}, url) {
		proc.Sync()
		measurements := in.rt.Measure(proc.Log().Tail(0))
		in.update(func(r *v1.Instance) { r.Measurements = mergeMeasurements(r.Measurements, measurements) })
		m.Log.Info("adopted running instance", "name", rec.GetName(), "pid", rec.GetPid(), "endpoint", rec.GetEndpoint())
		// Probe missing or failed template results asynchronously during recovery.
		if rec.GetTemplate() == nil || rec.GetTemplate().GetError() != "" {
			go func() {
				probe := m.probeTemplate(ctx, func(format string, args ...any) { m.Log.Info(fmt.Sprintf(format, args...), "name", rec.GetName()) }, in)
				in.update(func(r *v1.Instance) { r.Template = probe })
			}()
		}
		return
	}
	in.update(func(r *v1.Instance) {
		r.State = v1.InstanceState_INSTANCE_STATE_STARTING
		r.ReadyAt = nil
	})
	m.Log.Info("adopted instance still starting", "name", rec.GetName(), "pid", rec.GetPid())
	task := m.Tasks.Start(kindRun, "adopt "+rec.GetName(), map[string]string{"instance": rec.GetId(), "name": rec.GetName()}, func(ctx context.Context, h *tasks.Handle) error {
		h.Logf("adopted pid %d left running by the previous daemon", rec.GetPid())
		if err := m.waitReady(ctx, h, in, proc); err != nil {
			return err
		}
		proc.Sync()
		m.ready(h, in, in.rt.Measure(proc.Log().Tail(0)), m.probeTemplate(ctx, h.Logf, in))
		return nil
	})
	in.update(func(r *v1.Instance) { r.TaskId = task.GetId() })
}

// Relaunches serially so each plan accounts for earlier launches.
func (m *Manager) relaunch(ctx context.Context, list []*instance) {
	for _, old := range list {
		if ctx.Err() != nil {
			return
		}
		rec := old.snapshot()
		req := rec.GetRequest()
		if req == nil {
			m.Log.Warn("cannot relaunch, record has no request", "name", rec.GetName())
			continue
		}
		m.Log.Info("relaunching", "name", rec.GetName())
		_, task, err := m.Run(ctx, req)
		if err != nil {
			m.Log.Warn("relaunch failed", "name", rec.GetName(), "err", err)
			old.update(func(r *v1.Instance) { r.Error = "relaunch failed: " + err.Error() })
			continue
		}
		old.update(func(r *v1.Instance) { r.DesiredRunning = false })
		m.Tasks.Watch(ctx, task.GetId(), func(*v1.WatchTaskResponse) error { return nil })
	}
}

func (m *Manager) load(ctx context.Context) ([]*instance, error) {
	records, err := m.DB.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*instance, 0, len(records))
	for _, rec := range records {
		rt, err := m.Runtimes.Get(rec.GetRuntimeId())
		if err != nil {
			rt = nil
		}
		out = append(out, m.newInstance(rec, rt))
	}
	return out, nil
}

func (m *Manager) forget(id string) {
	if err := m.DB.DeleteInstance(context.Background(), id); err != nil {
		m.Log.Warn("instance record delete failed", "id", id, "err", err)
	}
	os.Remove(m.logPath(id))
}

func (m *Manager) logPath(id string) string { return filepath.Join(m.Dir, id+".log") }

func (m *Manager) pruneLocked() {
	finished := 0
	for _, in := range m.list {
		if Terminal(in.snapshot().GetState()) {
			finished++
		}
	}
	for i := 0; i < len(m.list) && finished > historyMax; i++ {
		in := m.list[i]
		if Terminal(in.snapshot().GetState()) {
			m.list = append(m.list[:i], m.list[i+1:]...)
			m.forget(in.rec.GetId())
			finished--
			i--
		}
	}
}

// Returns the stored descriptor, rebuilding missing or outdated descriptors from local files.
func (m *Manager) describe(ctx context.Context, stored *v1.StoredModel) (*v1.Descriptor, error) {
	if d := stored.GetDescriptor_(); d != nil && d.GetKind() != v1.ModelKind_MODEL_KIND_UNSPECIFIED {
		return d, nil
	}
	return m.rebuild(ctx, stored)
}

// Backfills descriptors and runtime masks in old manifest
func (m *Manager) RefreshDescriptors(ctx context.Context) {
	list, err := m.Store.ListManifests()
	if err != nil {
		m.Log.Warn("refresh descriptors", "err", err)
		return
	}
	for _, stored := range list {
		if ctx.Err() != nil {
			return
		}
		d := stored.GetDescriptor_()
		if d == nil || d.GetKind() == v1.ModelKind_MODEL_KIND_UNSPECIFIED || diffusion.Undetected(d) {
			var err error
			if d, err = m.rebuild(ctx, stored); err != nil {
				m.Log.Warn("refresh descriptor", "repo", stored.GetRepo(), "group", stored.GetGroup(), "err", err)
				continue
			}
		}
		mask := m.Runtimes.Mask(stored.GetFormatId(), d.GetKind())
		if proto.Equal(d, stored.GetDescriptor_()) && stored.GetRuntimes() == mask {
			continue
		}
		key := store.Key(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup())
		unlock := m.Store.Lock(key)
		stored.Descriptor_ = d
		stored.Runtimes = mask
		err = m.Store.WriteManifest(stored)
		unlock()
		if err != nil {
			m.Log.Warn("refresh descriptor", "repo", stored.GetRepo(), "group", stored.GetGroup(), "err", err)
			continue
		}
		m.Events.Publish(v1.EventKind_EVENT_KIND_MODEL, v1.EventAction_EVENT_ACTION_UPDATED, key, stored)
		m.Log.Info("descriptor refreshed", "repo", stored.GetRepo(), "group", stored.GetGroup(), "kind", strings.ToLower(strings.TrimPrefix(d.GetKind().String(), "MODEL_KIND_")), "architecture", d.GetArchitecture())
	}
}

// Reads a stored model's headers off the disk and builds its descriptor
func (m *Manager) rebuild(ctx context.Context, stored *v1.StoredModel) (*v1.Descriptor, error) {
	f := m.Inspector.Formats.Get(stored.GetFormatId())
	if f == nil {
		return nil, fmt.Errorf("no format reads %q", stored.GetFormatId())
	}
	g := &formats.Group{FormatID: stored.GetFormatId(), Name: stored.GetGroup(), Files: map[v1.ArtifactRole][]*v1.Artifact{}}
	for _, sa := range stored.GetArtifacts() {
		a := proto.Clone(sa.GetArtifact()).(*v1.Artifact)
		a.Path = sa.GetPath()
		if a.GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS {
			g.Weights = append(g.Weights, a)
		} else {
			g.Files[a.GetRole()] = append(g.Files[a.GetRole()], a)
		}
	}
	open := func(ctx context.Context, a *v1.Artifact) (sources.Blob, error) { return sources.OpenFile(a.GetPath()) }
	raw, err := f.Read(ctx, open, g)
	if err != nil {
		return nil, err
	}
	return m.Inspector.Builder.Build(raw)
}

func (m *Manager) artifacts(stored *v1.StoredModel, rt runtimes.Runtime) map[string]string {
	out := map[string]string{"weights_dir": stored.GetPath()}
	for _, sa := range stored.GetArtifacts() {
		key := text.Enum(sa.GetArtifact().GetRole())
		if _, exists := out[key]; !exists {
			out[key] = sa.GetPath()
		}
	}
	if m.Store != nil {
		if dir, err := m.Store.PreparedDir(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup(), rt.ID()); err == nil {
			out["prepared_dir"] = dir
		} else {
			m.Log.Warn("prepared dir", "err", err)
		}
	}
	return out
}

// Restricts the profile to slot devices and caps placement memory pools at its budget.
func Constrain(p *v1.HostProfile, deviceIDs []string, budget uint64, placement v1.Placement) *v1.HostProfile {
	out := proto.Clone(p).(*v1.HostProfile)
	if len(deviceIDs) == 0 && budget == 0 {
		return out
	}
	hostOnly := placement == v1.Placement_PLACEMENT_HOST
	allowed := map[string]bool{}
	for _, id := range deviceIDs {
		allowed[id] = true
	}
	var devices []*v1.Device
	for _, d := range out.GetDevices() {
		if len(allowed) == 0 || allowed[d.GetId()] || d.GetKind() == v1.DeviceKind_DEVICE_KIND_CPU {
			devices = append(devices, d)
		}
	}
	out.Devices = devices
	var pools []*v1.MemoryPool
	for _, pl := range out.GetPools() {
		device := pl.GetKind() == v1.PoolKind_POOL_KIND_DEVICE || pl.GetKind() == v1.PoolKind_POOL_KIND_UNIFIED
		if device && len(allowed) > 0 && !allowed[pl.GetDeviceId()] && !allowed[pl.GetId()] {
			continue
		}
		capped := device
		if hostOnly {
			capped = pl.GetKind() == v1.PoolKind_POOL_KIND_HOST || pl.GetKind() == v1.PoolKind_POOL_KIND_UNIFIED
		}
		if capped && budget > 0 {
			pl.TotalBytes = min(pl.GetTotalBytes(), budget)
			pl.FreeBytes = min(pl.GetFreeBytes(), budget)
		}
		pools = append(pools, pl)
	}
	out.Pools = pools
	return out
}

// Device pins from the slot, or none.
func slotDevices(p *v1.HostProfile, res *Reservation) []*v1.Device {
	if res == nil || len(res.DeviceIDs) == 0 {
		return nil
	}
	var out []*v1.Device
	for _, d := range p.GetDevices() {
		if d.GetKind() != v1.DeviceKind_DEVICE_KIND_CPU {
			out = append(out, d)
		}
	}
	return out
}

func deviceFree(p *v1.HostProfile) int64 {
	var total int64
	for _, pl := range p.GetPools() {
		if pl.GetKind() == v1.PoolKind_POOL_KIND_DEVICE || pl.GetKind() == v1.PoolKind_POOL_KIND_UNIFIED {
			total += int64(pl.GetFreeBytes())
		}
	}
	return total
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", bindHost+":0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// Reports whether an instance state is final
func Terminal(s v1.InstanceState) bool {
	return s == v1.InstanceState_INSTANCE_STATE_STOPPED || s == v1.InstanceState_INSTANCE_STATE_FAILED
}

// Adopts a seat left running: its guard listener comes back on the port peers know, its readiness
// is checked by its role's health with evidence, a head's template is probed again, and only then
// is it ready
func (m *Manager) adoptSeat(ctx context.Context, in *instance, proc launch.Handle) {
	rec := in.snapshot()
	m.restoreGuard(in)
	if Terminal(in.snapshot().GetState()) {
		return
	}
	role, err := runtimes.RoleOf(in.rt, rec.GetSeat())
	if err != nil {
		m.fail(in, err)
		proc.Stop(in.grace())
		return
	}
	since := time.Now()
	in.update(func(r *v1.Instance) {
		r.State = v1.InstanceState_INSTANCE_STATE_STARTING
		r.ReadyAt = nil
	})
	m.Log.Info("adopted seat, verifying it", "name", rec.GetName(), "pid", rec.GetPid(), "formation", rec.GetSeat().GetFormationId())
	task := m.Tasks.Start(kindRun, "adopt "+rec.GetName(), map[string]string{"instance": rec.GetId(), "name": rec.GetName(), "formation": rec.GetSeat().GetFormationId(), "role": rec.GetSeat().GetRole()}, func(ctx context.Context, h *tasks.Handle) error {
		h.Logf("adopted pid %d left running by the previous daemon", rec.GetPid())
		if err := m.verifyAdopted(ctx, h, in, proc, since); err != nil {
			return err
		}
		proc.Sync()
		lines := proc.Log().Tail(0)
		transport := in.rt.Transport(lines)
		in.update(func(r *v1.Instance) { r.Transport = transport })
		if transport != "" {
			h.Logf("transport %s", transport)
		}
		var probe *v1.TemplateProbe
		if role.Head {
			probe = m.probeTemplate(ctx, h.Logf, in)
		}
		m.ready(h, in, in.rt.Measure(lines), probe)
		return nil
	})
	in.update(func(r *v1.Instance) { r.TaskId = task.GetId() })
}

// Checks an adopted seat by its role's health: an HTTP path answering, the process alive, a hello
// from a stage no client holds, or the conductor's record that the head is ready on a stage a
// head holds, since a stage in use cannot take a hello
func (m *Manager) verifyAdopted(ctx context.Context, h *tasks.Handle, in *instance, proc launch.Handle, since time.Time) error {
	rec := in.snapshot()
	health, err := runtimes.SeatHealth(in.rt, rec.GetSeat())
	if err != nil {
		return err
	}
	if health.Kind != runtimes.HealthHello {
		return m.waitSeatReady(ctx, h, in, proc)
	}
	seat := rec.GetSeat()
	g := in.guardOf()
	if g != nil && g.Active() == 0 {
		addr := net.JoinHostPort(bindHost, strconv.Itoa(int(seat.GetLocalPort())))
		h.Message("hello to the idle stage at " + addr)
		version, err := rpcHello(ctx, addr)
		if err != nil {
			return m.adoptFailed(h, in, proc, fmt.Errorf("no hello from the adopted stage at %s: %w", addr, err))
		}
		h.Logf("rpc server speaks protocol %s", version)
		return nil
	}
	holder := "its head holds the exposed stage"
	if g != nil {
		holder = fmt.Sprintf("%d connections hold the stage through its guard", g.Active())
	}
	h.Message("waiting for the conductor's record to confirm the head: " + holder)
	if m.Formations == nil {
		return m.adoptFailed(h, in, proc, errors.New(holder+", and this node has no conductor record to confirm the head by"))
	}
	cctx, cancel := context.WithTimeout(ctx, adoptConfirmWindow)
	defer cancel()
	if err := m.Formations.HeadReady(cctx, seat.GetFormationId(), since); err != nil {
		return m.adoptFailed(h, in, proc, fmt.Errorf("%s, and the conductor did not confirm the head on it: %w", holder, err))
	}
	h.Logf("the conductor's record confirms the head is ready on this stage")
	return nil
}

// Fails an adopted seat that did not verify, stopping its process and logging its triage
func (m *Manager) adoptFailed(h *tasks.Handle, in *instance, proc launch.Handle, err error) error {
	hits := m.fail(in, err)
	proc.Stop(in.grace())
	for _, hit := range hits {
		h.Logf("triage %s: %s. %s", hit.GetId(), hit.GetSummary(), hit.GetHint())
	}
	if len(hits) > 0 {
		return fmt.Errorf("%s: %s", hits[0].GetSummary(), hits[0].GetHint())
	}
	return err
}
