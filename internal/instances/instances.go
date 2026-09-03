// Package instances plans, launches, supervises, and routes running models.
package instances

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/calibrate"
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
	measureKey   = "device.used"
	closeTimeout = 30 * time.Second
)

// Returned when an instance id or name is not known
var ErrUnknownInstance = errors.New("unknown instance")

// Owns every instance of this daemon
type Manager struct {
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
	mu   sync.Mutex
	rec  *v1.Instance
	rt   *runtime.Runtime
	proc *launch.Process
	log  *launch.Log
}

func (in *instance) snapshot() *v1.Instance {
	in.mu.Lock()
	defer in.mu.Unlock()
	return proto.Clone(in.rec).(*v1.Instance)
}

func (in *instance) update(fn func(*v1.Instance)) {
	in.mu.Lock()
	defer in.mu.Unlock()
	fn(in.rec)
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
		install, err = m.Installs.Get(req.GetInstallId())
	} else {
		install, err = m.Installs.Default(rt.Manifest.GetId())
	}
	if err != nil {
		return nil, nil, err
	}
	name := req.GetName()
	if name == "" {
		name = path.Base(stored.GetRepo()) + ":" + stored.GetGroup()
	}
	if _, taken := m.Route(name); taken || m.starting(name) {
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
	in := &instance{rt: rt, rec: &v1.Instance{
		Id:        newID(),
		Name:      name,
		SourceId:  stored.GetSourceId(),
		Repo:      stored.GetRepo(),
		Group:     stored.GetGroup(),
		RuntimeId: rt.Manifest.GetId(),
		InstallId: install.GetId(),
		Params:    stringParams(params),
		Command:   append([]string{rendered.Command}, rendered.Args...),
		Endpoint:  fmt.Sprintf("http://%s:%d", bindHost, port),
		State:     v1.InstanceState_INSTANCE_STATE_STARTING,
		CreatedAt: timestamppb.Now(),
		Plan:      plan,
	}}
	m.mu.Lock()
	m.list = append(m.list, in)
	m.pruneLocked()
	m.mu.Unlock()
	task := m.Tasks.Start(kindRun, "run "+name, map[string]string{"instance": in.rec.Id, "name": name}, func(ctx context.Context, h *tasks.Handle) error {
		return m.start(ctx, h, in, rendered, install, descriptor, profile)
	})
	in.update(func(r *v1.Instance) { r.TaskId = task.GetId() })
	return in.snapshot(), task, nil
}

func (m *Manager) start(ctx context.Context, h *tasks.Handle, in *instance, rendered *runtime.Rendered, install *v1.Install, d *v1.Descriptor, before *v1.HostProfile) error {
	rec := in.snapshot()
	h.Logf("command: %s", strings.Join(rec.GetCommand(), " "))
	h.Progress(0, 0, "launching")
	proc, err := m.Launcher.Launch(ctx, launch.Spec{Command: rendered.Command, Args: rendered.Args, Env: rendered.Env, Dir: install.GetDir()})
	if err != nil {
		in.update(func(r *v1.Instance) {
			r.State = v1.InstanceState_INSTANCE_STATE_FAILED
			r.Error = err.Error()
			r.StoppedAt = timestamppb.Now()
		})
		return err
	}
	in.mu.Lock()
	in.proc, in.log = proc, proc.Log()
	in.rec.Pid = int32(proc.Pid())
	in.mu.Unlock()
	go m.supervise(in)
	health := in.rt.Manifest.GetLaunch().GetHealth()
	url := rec.GetEndpoint() + health.GetPath()
	h.Message("waiting for " + url)
	err = launch.WaitHealthy(ctx, proc, url, time.Duration(health.GetIntervalMs())*time.Millisecond, time.Duration(health.GetTimeoutMs())*time.Millisecond)
	if err != nil {
		hits := m.fail(in, err)
		for _, hit := range hits {
			h.Logf("triage %s: %s. %s", hit.GetId(), hit.GetSummary(), hit.GetHint())
		}
		if ctx.Err() == nil {
			proc.Stop(in.rt.StopGrace())
		}
		if len(hits) > 0 {
			return fmt.Errorf("%s: %s", hits[0].GetSummary(), hits[0].GetHint())
		}
		return err
	}
	measurements := in.rt.Measure(proc.Log().Tail(0))
	if after, perr := m.Host.Profile(ctx, true); perr == nil {
		used := deviceFree(before) - deviceFree(after)
		if used > 0 {
			measurements = append(measurements, &v1.Measurement{Key: measureKey, Bytes: uint64(used)})
			if rec.GetPlan() != nil {
				if cerr := m.Calibration.Record(in.rt.Manifest.GetId(), d.GetArchitecture(), uint64(used), estimate.PlannedDevice(rec.GetPlan())); cerr != nil {
					m.Log.Warn("calibration write failed", "err", cerr)
				}
			}
		}
	}
	in.update(func(r *v1.Instance) {
		if r.State == v1.InstanceState_INSTANCE_STATE_STARTING {
			r.State = v1.InstanceState_INSTANCE_STATE_READY
			r.ReadyAt = timestamppb.Now()
		}
		r.Measurements = measurements
	})
	for _, ms := range measurements {
		h.Logf("%s %s", ms.GetKey(), estimate.Human(ms.GetBytes()))
	}
	h.Progress(1, 1, "ready at "+rec.GetEndpoint())
	h.Logf("ready %s at %s", rec.GetName(), rec.GetEndpoint())
	return nil
}

// Marks an instance failed and returns triage hits
func (m *Manager) fail(in *instance, err error) []*v1.TriageHit {
	var hits []*v1.TriageHit
	if in.log != nil {
		hits = m.Triage.Scan(in.rt.Manifest.GetTriage(), in.log.Tail(0))
	}
	in.update(func(r *v1.Instance) {
		if r.State == v1.InstanceState_INSTANCE_STATE_STOPPING || r.State == v1.InstanceState_INSTANCE_STATE_STOPPED {
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
	<-in.proc.Done()
	exitErr := in.proc.Err()
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

// Stops an instance and waits for it to exit
func (m *Manager) Stop(ctx context.Context, id string) (*v1.Instance, error) {
	in, err := m.find(id)
	if err != nil {
		return nil, err
	}
	in.mu.Lock()
	proc := in.proc
	if !terminal(in.rec.State) {
		in.rec.State = v1.InstanceState_INSTANCE_STATE_STOPPING
	}
	in.mu.Unlock()
	if proc != nil {
		proc.Stop(in.rt.StopGrace())
		select {
		case <-proc.Done():
		case <-ctx.Done():
			return in.snapshot(), ctx.Err()
		}
	}
	in.update(func(r *v1.Instance) {
		if !terminal(r.State) || r.State == v1.InstanceState_INSTANCE_STATE_STOPPING {
			r.State = v1.InstanceState_INSTANCE_STATE_STOPPED
			r.StoppedAt = timestamppb.Now()
		}
	})
	return in.snapshot(), nil
}

// Streams runtime output, following until exit when asked
func (m *Manager) Logs(ctx context.Context, id string, follow bool, tail int, send func([]string) error) error {
	in, err := m.find(id)
	if err != nil {
		return err
	}
	in.mu.Lock()
	log := in.log
	in.mu.Unlock()
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

func (m *Manager) starting(name string) bool {
	for _, rec := range m.List(true) {
		if rec.GetName() == name {
			return true
		}
	}
	return false
}

// Stops every live instance
func (m *Manager) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, rec := range m.List(true) {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			m.Stop(ctx, id)
		}(rec.GetId())
	}
	wg.Wait()
}

func (m *Manager) pruneLocked() {
	finished := 0
	for _, in := range m.list {
		if terminal(in.snapshot().GetState()) {
			finished++
		}
	}
	for i := 0; i < len(m.list) && finished > historyMax; i++ {
		if terminal(m.list[i].snapshot().GetState()) {
			m.list = append(m.list[:i], m.list[i+1:]...)
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

func stringParams(params map[string]any) map[string]string {
	out := make(map[string]string, len(params))
	for k, v := range params {
		out[k] = fmt.Sprint(v)
	}
	return out
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
