// Package instances runs, supervises, records, recovers, and routes models.
package instances

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/nickheyer/nebu/pkg/eval"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/launch"
	"github.com/nickheyer/nebu/pkg/proc"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtime"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
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
	restartNote  = "daemon restarted"
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
	RuntimeID   string
	Params      map[string]string
	InstanceID  string
}

type swapKey struct{}

// Marks a context as a swap, allowed beside an occupant
func WithSwap(ctx context.Context) context.Context { return context.WithValue(ctx, swapKey{}, true) }

func fromSwap(ctx context.Context) bool { v, _ := ctx.Value(swapKey{}).(bool); return v }

// Resolves slot ids to reservations, implemented by the slot manager
type Reserver interface {
	Reservation(ctx context.Context, slotID string) (*Reservation, error)
}

// Owns every instance of this daemon
type Manager struct {
	DB          *db.DB
	Dir         string
	Store       *store.Store
	Runtimes    *runtime.Registry
	Installs    *installs.Manager
	Inspector   *inspect.Inspector
	Launcher    launch.Launcher
	Tasks       *tasks.Manager
	Host        *host.Prober
	Triage      *triage.Matcher
	Calibration *calibrate.Table
	Routes      *gateway.Table
	Events      *events.Bus
	Reserver    Reserver
	Log         *slog.Logger

	// Called after every state change, outside the instance lock
	OnChange func(*v1.Instance)

	mu   sync.Mutex
	list []*instance
}

type instance struct {
	mgr      *Manager
	mu       sync.Mutex
	rec      *v1.Instance
	rt       *runtime.Runtime
	proc     launch.Handle
	log      *launch.Log
	exited   chan struct{}
	exitOnce sync.Once
}

func (m *Manager) newInstance(rec *v1.Instance, rt *runtime.Runtime) *instance {
	in := &instance{mgr: m, rec: rec, rt: rt, exited: make(chan struct{})}
	if terminal(rec.GetState()) {
		in.exitOnce.Do(func() { close(in.exited) })
	}
	return in
}

func (in *instance) snapshot() *v1.Instance {
	in.mu.Lock()
	defer in.mu.Unlock()
	return proto.Clone(in.rec).(*v1.Instance)
}

// Applies fn, writes the record, releases waiters, and notifies
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
	// Stop waiters continue only after observers saw the exit
	if terminal(rec.GetState()) {
		in.exitOnce.Do(func() { close(in.exited) })
	}
}

// Updates routes for a changed instance and tells listeners
func (m *Manager) changed(rec *v1.Instance, before v1.InstanceState) {
	if m.Routes != nil && rec.GetSlotId() == "" {
		switch {
		case rec.GetState() == v1.InstanceState_INSTANCE_STATE_READY:
			m.Routes.Set(rec.GetName(), rec.GetId(), "", rec.GetEndpoint(), modelOf(rec), m.api(rec), nil)
		case rec.GetState() == v1.InstanceState_INSTANCE_STATE_DRAINING && before != v1.InstanceState_INSTANCE_STATE_DRAINING:
			m.Routes.Drain(rec.GetId())
		case terminal(rec.GetState()) && !terminal(before):
			m.Routes.RemoveInstance(rec.GetId())
		}
	} else if m.Routes != nil && terminal(rec.GetState()) && !terminal(before) {
		m.Routes.RemoveInstance(rec.GetId())
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_INSTANCE, v1.EventAction_EVENT_ACTION_UPDATED, rec.GetId(), &v1.Event_Instance{Instance: rec})
	if m.OnChange != nil {
		m.OnChange(rec)
	}
}

// Returns the wire protocol of an instance's runtime
func (m *Manager) api(rec *v1.Instance) v1.ApiFlavor {
	return m.Runtimes.API(rec.GetRuntimeId())
}

// Formats the model an instance serves for routes
func modelOf(rec *v1.Instance) string {
	return rec.GetRepo() + ":" + rec.GetGroup()
}

func (in *instance) grace() time.Duration {
	if in.rt != nil {
		return in.rt.StopGrace()
	}
	return runtime.DefaultStopGrace
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

// Everything resolved for a run before anything is launched
const defaultPrepareTimeout = time.Hour

type prepared struct {
	req        *v1.RunRequest
	stored     *v1.StoredModel
	rt         *runtime.Runtime
	install    *v1.Install
	name       string
	descriptor *v1.Descriptor
	profile    *v1.HostProfile
	planned    *v1.HostProfile
	res        *Reservation
	params     map[string]any
	plan       *v1.MemoryPlan
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
		if cur, err := m.Get(p.res.InstanceID); err == nil && !terminal(cur.GetState()) {
			return nil, nil, fmt.Errorf("%w: slot %s serves %s, use nebu swap", runtime.ErrParam, p.res.Name, cur.GetName())
		}
	}
	if p.plan != nil && p.plan.GetVerdict() == v1.FitVerdict_FIT_VERDICT_NO && !p.req.GetForce() {
		return nil, nil, fmt.Errorf("%w: %s does not fit, %s; pass force to run anyway", runtime.ErrParam, p.name, p.plan.GetDetail())
	}
	return m.launch(ctx, p)
}

// Resolves model, runtime, install, reservation, and plan
func (m *Manager) prepare(ctx context.Context, req *v1.RunRequest) (*prepared, error) {
	req = proto.Clone(req).(*v1.RunRequest)
	stored, err := m.Store.ReadManifest(req.GetSourceId(), req.GetRepo(), req.GetGroup())
	if err != nil {
		return nil, err
	}
	var res *Reservation
	if req.GetSlotId() != "" {
		if m.Reserver == nil {
			return nil, fmt.Errorf("%w: slots are not available", runtime.ErrParam)
		}
		if res, err = m.Reserver.Reservation(ctx, req.GetSlotId()); err != nil {
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
	// A named profile picks its runtime when nothing else did
	if req.GetRuntimeId() == "" && req.GetProfileId() != "" {
		p, err := m.Inspector.Profile("", req.GetProfileId())
		if err != nil {
			return nil, err
		}
		req.RuntimeId = p.GetRuntimeId()
	}
	if req.GetRuntimeId() == "" {
		if req.RuntimeId, err = m.defaultRuntime(ctx, stored.GetFormatId()); err != nil {
			return nil, err
		}
	}
	rt, err := m.Runtimes.Get(req.GetRuntimeId())
	if err != nil {
		return nil, err
	}
	if !rt.Accepts(stored.GetFormatId()) {
		return nil, fmt.Errorf("%w: runtime %s does not accept %s", runtime.ErrParam, rt.Manifest.GetId(), stored.GetFormatId())
	}
	var install *v1.Install
	if req.GetInstallId() != "" {
		install, err = m.Installs.Get(ctx, req.GetInstallId())
	} else {
		install, err = m.Installs.Default(ctx, rt.Manifest.GetId())
	}
	if err != nil {
		return nil, err
	}
	name := req.GetName()
	if name == "" {
		name = path.Base(stored.GetRepo()) + ":" + stored.GetGroup()
	}
	if m.conflict(name, req.GetSlotId()) {
		return nil, fmt.Errorf("%w: instance %q is already running", runtime.ErrParam, name)
	}
	profile, err := m.Host.Profile(ctx, true)
	if err != nil {
		return nil, err
	}
	planProfile := profile
	if res != nil {
		planProfile = Constrain(profile, res.DeviceIDs, res.MemoryBytes)
	}
	var slotParams map[string]string
	if res != nil {
		slotParams = res.Params
	}
	// The one layering every plan uses, and a named profile is kept by id so a rename cannot strand it
	used, layered, err := m.Inspector.Layer(rt.Manifest.GetId(), req.GetProfileId(), slotParams, req.GetParams())
	if err != nil {
		return nil, err
	}
	if req.GetProfileId() != "" {
		req.ProfileId = used.GetId()
	}
	params, err := rt.Params(layered)
	if err != nil {
		return nil, err
	}
	p := &prepared{req: req, stored: stored, rt: rt, install: install, name: name, descriptor: descriptor, profile: profile, planned: planProfile, res: res, params: params}
	if rt.Policy != nil {
		if p.plan, err = m.Inspector.Plan(rt, descriptor, planProfile, layered, true); err != nil {
			return nil, err
		}
		for k, v := range p.plan.GetParams() {
			if params[k] == runtime.Auto {
				params[k] = v
			}
		}
	}
	return p, nil
}

// Renders and launches a prepared run
func (m *Manager) launch(ctx context.Context, p *prepared) (*v1.Instance, *v1.Task, error) {
	req, stored, rt, install, name, descriptor, profile, params, plan := p.req, p.stored, p.rt, p.install, p.name, p.descriptor, p.profile, p.params, p.plan
	port, err := freePort()
	if err != nil {
		return nil, nil, err
	}
	// A model that just launched is the last the store evicts, a plan alone changes nothing
	if err := m.Store.Touch(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup()); err != nil {
		m.Log.Warn("store touch failed", "repo", stored.GetRepo(), "err", err)
	}
	artifacts := m.artifacts(stored, rt)
	input := runtime.RenderInput{
		Name:       name,
		Params:     params,
		Artifacts:  artifacts,
		Host:       bindHost,
		Port:       port,
		Install:    map[string]string{"path": install.GetPath(), "dir": install.GetDir(), "version": install.GetVersion()},
		Devices:    deviceViews(p.planned, p.res),
		Descriptor: descriptor,
	}
	var prep *runtime.Rendered
	if rt.Prepares(stored.GetFormatId()) {
		if prep, err = rt.RenderPrepare(input); err != nil {
			return nil, nil, err
		}
		launchArtifacts := make(map[string]string, len(artifacts))
		for k, v := range artifacts {
			launchArtifacts[k] = v
		}
		launchArtifacts["weights_dir"] = artifacts["prepared_dir"]
		input.Artifacts = launchArtifacts
	}
	rendered, err := rt.Render(input)
	if err != nil {
		return nil, nil, err
	}
	in := m.newInstance(&v1.Instance{
		Id:             newID(),
		Name:           name,
		SourceId:       stored.GetSourceId(),
		Repo:           stored.GetRepo(),
		Group:          stored.GetGroup(),
		RuntimeId:      rt.Manifest.GetId(),
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
		return nil, nil, fmt.Errorf("%w: instance %q is already running", runtime.ErrParam, name)
	}
	m.list = append(m.list, in)
	m.pruneLocked()
	m.mu.Unlock()
	m.Events.Publish(v1.EventKind_EVENT_KIND_INSTANCE, v1.EventAction_EVENT_ACTION_CREATED, in.rec.GetId(), &v1.Event_Instance{Instance: in.snapshot()})
	prepDir := artifacts["prepared_dir"]
	prepTimeout := time.Duration(rt.Manifest.GetLaunch().GetPrepare().GetTimeoutMs()) * time.Millisecond
	task := m.Tasks.Start(kindRun, "run "+name, map[string]string{"instance": in.rec.GetId(), "name": name}, func(ctx context.Context, h *tasks.Handle) error {
		if prep != nil {
			if err := m.runPrepare(ctx, h, in, prep, install.GetDir(), prepDir, prepTimeout); err != nil {
				return err
			}
		}
		return m.start(ctx, h, in, rendered, install, descriptor, profile)
	})
	in.update(func(r *v1.Instance) { r.TaskId = task.GetId() })
	return in.snapshot(), task, nil
}

func (m *Manager) runPrepare(ctx context.Context, h *tasks.Handle, in *instance, prep *runtime.Rendered, dir, prepDir string, timeout time.Duration) error {
	marker := filepath.Join(prepDir, ".prepared")
	if _, err := os.Stat(marker); err == nil {
		h.Logf("prepared earlier at %s", prepDir)
		return nil
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
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintln(logFile, line)
			h.Logf("%s", line)
		}
	}()
	// A converter forks workers, so a timeout or cancel takes the whole tree
	err = proc.Run(cmd)
	pw.Close()
	<-done
	if err != nil {
		os.RemoveAll(prepDir)
		os.MkdirAll(prepDir, 0o755)
		in.update(func(r *v1.Instance) {
			if terminal(r.State) {
				return
			}
			r.State = v1.InstanceState_INSTANCE_STATE_FAILED
			r.Error = "prepare: " + err.Error()
			r.StoppedAt = timestamppb.Now()
		})
		return fmt.Errorf("prepare: %w", err)
	}
	return os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

func (m *Manager) start(ctx context.Context, h *tasks.Handle, in *instance, rendered *runtime.Rendered, install *v1.Install, d *v1.Descriptor, before *v1.HostProfile) error {
	rec := in.snapshot()
	h.Logf("command: %s", strings.Join(rec.GetCommand(), " "))
	h.Progress(0, 0, "launching")
	proc, err := m.Launcher.Launch(ctx, launch.Spec{Command: rendered.Command, Args: rendered.Args, Env: rendered.Env, Dir: install.GetDir(), LogPath: m.logPath(rec.GetId())})
	if err != nil {
		in.update(func(r *v1.Instance) {
			if terminal(r.State) {
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
			if rec.GetPlan() != nil {
				if cerr := m.Calibration.Record(ctx, in.rt.Manifest.GetId(), d.GetArchitecture(), uint64(used), estimate.PlannedDevice(rec.GetPlan())); cerr != nil {
					m.Log.Warn("calibration write failed", "err", cerr)
				}
			}
		}
	}
	m.ready(h, in, measurements)
	return nil
}

// Polls health, recording triage and stopping the process on failure
func (m *Manager) waitReady(ctx context.Context, h *tasks.Handle, in *instance, proc launch.Handle) error {
	health := in.rt.Manifest.GetLaunch().GetHealth()
	url := in.snapshot().GetEndpoint() + health.GetPath()
	h.Message("waiting for " + url)
	err := launch.WaitHealthy(ctx, proc, url, time.Duration(health.GetIntervalMs())*time.Millisecond, time.Duration(health.GetTimeoutMs())*time.Millisecond)
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

// Marks an instance ready unless stopped and reports measurements
func (m *Manager) ready(h *tasks.Handle, in *instance, measurements []*v1.Measurement) {
	var rec *v1.Instance
	in.update(func(r *v1.Instance) {
		if r.State == v1.InstanceState_INSTANCE_STATE_STARTING {
			r.State = v1.InstanceState_INSTANCE_STATE_READY
			r.ReadyAt = timestamppb.Now()
		}
		r.Measurements = mergeMeasurements(r.Measurements, measurements)
		rec = proto.Clone(r).(*v1.Instance)
	})
	for _, ms := range measurements {
		h.Logf("%s %s", ms.GetKey(), estimate.Human(ms.GetBytes()))
	}
	h.Progress(1, 1, "ready at "+rec.GetEndpoint())
	h.Logf("ready %s at %s", rec.GetName(), rec.GetEndpoint())
}

// Replaces measurements by key, keeping ones only old has
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
		hits = m.Triage.Scan(in.rt.Manifest.GetTriage(), log.Tail(0))
	}
	in.update(func(r *v1.Instance) {
		if terminal(r.State) || r.State == v1.InstanceState_INSTANCE_STATE_STOPPING {
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
		if runningOnly && terminal(rec.GetState()) {
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
		if !terminal(rec.GetState()) {
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
		if rec.GetName() == name && !terminal(rec.GetState()) {
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
		if rec.GetName() != name || terminal(rec.GetState()) {
			continue
		}
		if slotID == "" || rec.GetSlotId() != slotID {
			return true
		}
	}
	return false
}

// Picks the first compatible runtime accepting a format
func (m *Manager) defaultRuntime(ctx context.Context, formatID string) (string, error) {
	profile, err := m.Host.Profile(ctx, false)
	if err != nil {
		return "", err
	}
	for _, rt := range m.Runtimes.List() {
		if ok, _ := rt.Compatible(profile); ok && rt.Accepts(formatID) {
			return rt.Manifest.GetId(), nil
		}
	}
	return "", fmt.Errorf("%w: no compatible runtime accepts %s", runtime.ErrParam, formatID)
}

// Names the instances whose request starts from a profile, live ones and any a
// restart would relaunch, dropping the reference when clear is set
func (m *Manager) ProfileReferrers(refers func(ref, runtimeID string) bool, clear bool) []string {
	m.mu.Lock()
	list := append([]*instance(nil), m.list...)
	m.mu.Unlock()
	var out []string
	for _, in := range list {
		rec := in.snapshot()
		req := rec.GetRequest()
		if terminal(rec.GetState()) && !rec.GetDesiredRunning() || !refers(req.GetProfileId(), req.GetRuntimeId()) {
			continue
		}
		out = append(out, "instance "+rec.GetName())
		if clear {
			in.update(func(r *v1.Instance) { r.Request.ProfileId = "" })
		}
	}
	return out
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
		wasTerminal = terminal(r.State)
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

// Returns the endpoint of a ready instance by name
func (m *Manager) Route(name string) (string, bool) {
	for _, rec := range m.Ready() {
		if rec.GetName() == name || rec.GetId() == name {
			return rec.GetEndpoint(), true
		}
	}
	return "", false
}

// Lists instances that answer requests
func (m *Manager) Ready() []*v1.Instance {
	var out []*v1.Instance
	for _, rec := range m.List(true) {
		if rec.GetState() == v1.InstanceState_INSTANCE_STATE_READY {
			out = append(out, rec)
		}
	}
	return out
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

// Loads records, adopts live runtimes, marks the rest, relaunches wanted
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
		if !terminal(rec.GetState()) {
			alive := proc.Running(int(rec.GetPid()), rec.GetCommand())
			switch {
			case alive && in.rt != nil:
				m.adopt(ctx, in)
				continue
			case alive:
				m.Log.Warn("stopping instance whose runtime manifest is gone", "name", rec.GetName(), "runtime", rec.GetRuntimeId(), "pid", rec.GetPid())
				wg.Add(1)
				go func(pid int, grace time.Duration) {
					defer wg.Done()
					proc.Terminate(pid, grace)
				}(int(rec.GetPid()), in.grace())
			}
			in.update(func(r *v1.Instance) {
				r.State = v1.InstanceState_INSTANCE_STATE_STOPPED
				r.Error = restartNote
				r.StoppedAt = timestamppb.Now()
			})
		}
		if rec.GetDesiredRunning() && rec.GetState() != v1.InstanceState_INSTANCE_STATE_FAILED {
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
	health := in.rt.Manifest.GetLaunch().GetHealth()
	url := rec.GetEndpoint() + health.GetPath()
	if rec.GetState() == v1.InstanceState_INSTANCE_STATE_READY && launch.Healthy(&http.Client{Timeout: probeTimeout}, url) {
		proc.Sync()
		measurements := in.rt.Measure(proc.Log().Tail(0))
		in.update(func(r *v1.Instance) { r.Measurements = mergeMeasurements(r.Measurements, measurements) })
		m.Log.Info("adopted running instance", "name", rec.GetName(), "pid", rec.GetPid(), "endpoint", rec.GetEndpoint())
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
		m.ready(h, in, in.rt.Measure(proc.Log().Tail(0)))
		return nil
	})
	in.update(func(r *v1.Instance) { r.TaskId = task.GetId() })
}

// Relaunches serially so each plans around the last
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
		if terminal(in.snapshot().GetState()) {
			finished++
		}
	}
	for i := 0; i < len(m.list) && finished > historyMax; i++ {
		in := m.list[i]
		if terminal(in.snapshot().GetState()) {
			m.list = append(m.list[:i], m.list[i+1:]...)
			m.forget(in.rec.GetId())
			finished--
			i--
		}
	}
}

// Returns the stored descriptor or rebuilds it from local files
func (m *Manager) describe(ctx context.Context, stored *v1.StoredModel) (*v1.Descriptor, error) {
	if stored.GetDescriptor_() != nil {
		return stored.GetDescriptor_(), nil
	}
	reader, ok := m.Inspector.Readers[stored.GetFormatId()]
	if !ok {
		return nil, fmt.Errorf("no reader for format %q", stored.GetFormatId())
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
	raw, err := reader.Read(ctx, open, g)
	if err != nil {
		return nil, err
	}
	return m.Inspector.Builder.Build(raw)
}

func (m *Manager) artifacts(stored *v1.StoredModel, rt *runtime.Runtime) map[string]string {
	out := map[string]string{"weights_dir": stored.GetPath()}
	for _, sa := range stored.GetArtifacts() {
		key := eval.EnumShort(sa.GetArtifact().GetRole())
		if _, exists := out[key]; !exists {
			out[key] = sa.GetPath()
		}
	}
	if m.Store != nil {
		if dir, err := m.Store.PreparedDir(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup(), rt.Manifest.GetId()); err == nil {
			out["prepared_dir"] = dir
		} else {
			m.Log.Warn("prepared dir", "err", err)
		}
	}
	return out
}

// Narrows a profile to devices and caps pools at budget
func Constrain(p *v1.HostProfile, deviceIDs []string, budget uint64) *v1.HostProfile {
	out := proto.Clone(p).(*v1.HostProfile)
	if len(deviceIDs) == 0 && budget == 0 {
		return out
	}
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
		if device && budget > 0 {
			pl.TotalBytes = min(pl.GetTotalBytes(), budget)
			pl.FreeBytes = min(pl.GetFreeBytes(), budget)
		}
		pools = append(pools, pl)
	}
	out.Pools = pools
	return out
}

// Lists the devices a launch template may address
func deviceViews(p *v1.HostProfile, res *Reservation) []map[string]any {
	var out []map[string]any
	for _, d := range p.GetDevices() {
		if d.GetKind() == v1.DeviceKind_DEVICE_KIND_CPU {
			continue
		}
		facts := make(map[string]any, len(d.GetFacts()))
		for k, v := range d.GetFacts() {
			facts[k] = v
		}
		out = append(out, map[string]any{"id": d.GetId(), "kind": eval.EnumShort(d.GetKind()), "vendor": d.GetVendor(), "name": d.GetName(), "facts": facts})
	}
	if res == nil || len(res.DeviceIDs) == 0 {
		return nil
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

func terminal(s v1.InstanceState) bool {
	switch s {
	case v1.InstanceState_INSTANCE_STATE_STOPPED, v1.InstanceState_INSTANCE_STATE_FAILED:
		return true
	}
	return false
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
