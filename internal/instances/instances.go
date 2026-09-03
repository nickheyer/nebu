// Package instances plans, launches, supervises, records, recovers, and routes running models.
package instances

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/calibrate"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/eval"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/launch"
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
	Log         *slog.Logger

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

// Applies fn, writes the record, and releases stop waiters once terminal
func (in *instance) update(fn func(*v1.Instance)) {
	in.mu.Lock()
	defer in.mu.Unlock()
	fn(in.rec)
	if err := in.mgr.DB.PutInstance(context.Background(), in.rec); err != nil {
		in.mgr.Log.Warn("instance record write failed", "id", in.rec.GetId(), "err", err)
	}
	if terminal(in.rec.GetState()) {
		in.exitOnce.Do(func() { close(in.exited) })
	}
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

// Plans and launches a stored model, returning the record and its startup task
func (m *Manager) Run(ctx context.Context, req *v1.RunRequest) (*v1.Instance, *v1.Task, error) {
	stored, err := m.Store.ReadManifest(req.GetSourceId(), req.GetRepo(), req.GetGroup())
	if err != nil {
		return nil, nil, err
	}
	rt, err := m.Runtimes.Get(req.GetRuntimeId())
	if err != nil {
		return nil, nil, err
	}
	if !rt.Accepts(stored.GetFormatId()) {
		return nil, nil, fmt.Errorf("%w: runtime %s does not accept %s", runtime.ErrParam, rt.Manifest.GetId(), stored.GetFormatId())
	}
	var install *v1.Install
	if req.GetInstallId() != "" {
		install, err = m.Installs.Get(ctx, req.GetInstallId())
	} else {
		install, err = m.Installs.Default(ctx, rt.Manifest.GetId())
	}
	if err != nil {
		return nil, nil, err
	}
	name := req.GetName()
	if name == "" {
		name = path.Base(stored.GetRepo()) + ":" + stored.GetGroup()
	}
	if m.live(name) != nil {
		return nil, nil, fmt.Errorf("%w: instance %q is already running", runtime.ErrParam, name)
	}
	descriptor, err := m.describe(ctx, stored)
	if err != nil {
		return nil, nil, err
	}
	profile, err := m.Host.Profile(ctx, true)
	if err != nil {
		return nil, nil, err
	}
	params, err := rt.Params(req.GetParams())
	if err != nil {
		return nil, nil, err
	}
	var plan *v1.MemoryPlan
	if rt.Policy != nil {
		plan, err = rt.Policy.Plan(estimate.Input{
			Descriptor:    descriptor,
			Formulas:      m.Inspector.Builder.Formulas(descriptor.GetArchSpecId()),
			Host:          profile,
			Params:        params,
			Free:          true,
			OverheadDelta: m.Calibration.Delta(rt.Manifest.GetId(), descriptor.GetArchitecture()),
		})
		if err != nil {
			return nil, nil, err
		}
		if plan.GetVerdict() == v1.FitVerdict_FIT_VERDICT_NO {
			return nil, nil, fmt.Errorf("%w: %s does not fit, %s", runtime.ErrParam, name, plan.GetDetail())
		}
		for k, v := range plan.GetParams() {
			if params[k] == runtime.Auto {
				params[k] = v
			}
		}
	}
	port, err := freePort()
	if err != nil {
		return nil, nil, err
	}
	rendered, err := rt.Render(runtime.RenderInput{
		Name:      name,
		Params:    params,
		Artifacts: artifacts(stored),
		Host:      bindHost,
		Port:      port,
		Install:   map[string]string{"path": install.GetPath(), "dir": install.GetDir(), "version": install.GetVersion()},
	})
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
	}, rt)
	m.mu.Lock()
	if m.liveLocked(name) != nil {
		m.mu.Unlock()
		return nil, nil, fmt.Errorf("%w: instance %q is already running", runtime.ErrParam, name)
	}
	m.list = append(m.list, in)
	m.pruneLocked()
	m.mu.Unlock()
	task := m.Tasks.Start(kindRun, "run "+name, map[string]string{"instance": in.rec.GetId(), "name": name}, func(ctx context.Context, h *tasks.Handle) error {
		return m.start(ctx, h, in, rendered, install, descriptor, profile)
	})
	in.update(func(r *v1.Instance) { r.TaskId = task.GetId() })
	return in.snapshot(), task, nil
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

// Polls health, and on failure records triage, stops the process, and returns the reason
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

// Marks an instance ready unless a stop arrived meanwhile and reports measurements on the task
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

// Replaces measurements by key, keeping ones only the old set has
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

// Marks an instance failed unless already terminal and returns its triage hits
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
	case v1.InstanceState_INSTANCE_STATE_STOPPING:
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

// Stops an instance by request, clearing its relaunch intent, and waits for exit
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

// Returns the live log or loads the output file a finished instance left behind
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
		if rec.GetName() == name {
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

// Loads the store, adopts runtimes still alive, marks the rest stopped, and relaunches wanted ones
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
			alive := launch.Running(int(rec.GetPid()), rec.GetCommand())
			switch {
			case alive && in.rt != nil:
				m.adopt(ctx, in)
				continue
			case alive:
				m.Log.Warn("stopping instance whose runtime manifest is gone", "name", rec.GetName(), "runtime", rec.GetRuntimeId(), "pid", rec.GetPid())
				wg.Add(1)
				go func(pid int, grace time.Duration) {
					defer wg.Done()
					launch.Terminate(pid, grace)
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

// Takes over a runtime the previous daemon left running, routing it once it answers health
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

// Relaunches one instance at a time so each plans around the last
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

// Maps artifact roles onto the link paths a launch template can use
func artifacts(stored *v1.StoredModel) map[string]string {
	out := map[string]string{"weights_dir": stored.GetPath()}
	for _, sa := range stored.GetArtifacts() {
		key := eval.EnumShort(sa.GetArtifact().GetRole())
		if _, exists := out[key]; !exists {
			out[key] = sa.GetPath()
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
