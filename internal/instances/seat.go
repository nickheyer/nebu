package instances

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/launch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// A seat's log is read for its transport line this often, for this long after it is ready
	transportPoll   = 5 * time.Second
	transportWindow = 10 * time.Minute
	// A rank known by its process alone is ready once it has run this long
	processSettle = 3 * time.Second
)

// Resolves a seat launch: the runtime the conductor named, its install, the role, the files the
// role reads, the parameters, and the devices the plan gave the seat. The conductor planned the
// seat's memory, so no plan is made here.
func (m *Manager) prepareSeat(ctx context.Context, req *v1.RunRequest) (*prepared, error) {
	req = proto.Clone(req).(*v1.RunRequest)
	seat := req.GetSeat()
	if req.GetRuntimeId() == "" {
		return nil, fmt.Errorf("%w: a seat launch names its runtime", runtimes.ErrParam)
	}
	if seat.GetFormationId() == "" || seat.GetRole() == "" {
		return nil, fmt.Errorf("%w: a seat block names its formation and role", runtimes.ErrParam)
	}
	rt, err := m.Runtimes.Get(req.GetRuntimeId())
	if err != nil {
		return nil, err
	}
	role, err := runtimes.RoleOf(rt, seat)
	if err != nil {
		return nil, err
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
		name = fmt.Sprintf("%s/%s%d", seat.GetName(), role.Name, seat.GetRank())
		req.Name = name
	}
	if m.conflict(name, "") {
		return nil, fmt.Errorf("%w: instance %q is already running", runtimes.ErrParam, name)
	}
	profile, err := m.Host.Profile(ctx, true)
	if err != nil {
		return nil, err
	}
	p := &prepared{req: req, rt: rt, install: install, name: name, profile: profile, planned: profile, seat: seat, role: role}
	if role.Files == runtimes.FilesNone {
		if p.params, err = runtimes.Resolve(rt, req.GetParams()); err != nil {
			return nil, err
		}
	} else {
		stored, err := m.Store.ReadManifest(req.GetSourceId(), req.GetRepo(), req.GetGroup())
		if err != nil {
			return nil, fmt.Errorf("the %s seat needs %s %s on this node: %w", role.Name, req.GetRepo(), req.GetGroup(), err)
		}
		p.stored = stored
		if p.descriptor, err = m.describe(ctx, stored); err != nil {
			return nil, err
		}
		if !runtimes.Accepts(rt, stored.GetFormatId(), p.descriptor.GetKind()) {
			return nil, fmt.Errorf("%w: runtime %s does not serve %s %s models", runtimes.ErrParam, rt.ID(), stored.GetFormatId(), strings.ToLower(strings.TrimPrefix(p.descriptor.GetKind().String(), "MODEL_KIND_")))
		}
		if p.companions, err = m.Inspector.Companions(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup()); err != nil {
			return nil, err
		}
		layered, err := runtimes.ResolveStore(rt, req.GetParams(), append(append([]*v1.StoredModel{}, p.companions...), stored))
		if err != nil {
			return nil, err
		}
		if p.params, err = runtimes.Resolve(rt, layered); err != nil {
			return nil, err
		}
	}
	if seat.GetMemory() != nil {
		p.plan = seat.GetMemory()
		for k, v := range p.plan.GetParams() {
			if p.params.IsAuto(k) {
				p.params[k] = v
			}
		}
	}
	return p, nil
}

// The devices a seat pins on this node: the bare ids the plan lists, since an id qualified by a
// node names a device on another node, which the runtime renders from the seat block as a whole.
// Every device when the plan lists none
func seatDevices(profile *v1.HostProfile, ids []string) []*v1.Device {
	if len(ids) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, id := range ids {
		if strings.Contains(id, "/") {
			continue
		}
		want[id] = true
	}
	var out []*v1.Device
	for _, d := range profile.GetDevices() {
		if want[d.GetId()] {
			out = append(out, d)
		}
	}
	return out
}

// The host part of a seat's mesh address
func hostOf(address string) string {
	if h, _, err := net.SplitHostPort(address); err == nil {
		return h
	}
	return strings.Trim(address, "[]")
}

// A free port on the given host
func freePortOn(host string) (int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// The draft model's weights on this node, from the store key the seat block names
func (m *Manager) draftPath(key string) (string, error) {
	if key == "" {
		return "", nil
	}
	parts := strings.SplitN(key, "/", 3)
	if len(parts) != 3 {
		return "", fmt.Errorf("%w: the draft key %q is not source/repo/group", runtimes.ErrParam, key)
	}
	stored, err := m.Store.ReadManifest(parts[0], parts[1], parts[2])
	if err != nil {
		return "", fmt.Errorf("the draft model %s is not on this node: %w", key, err)
	}
	for _, sa := range stored.GetArtifacts() {
		if sa.GetArtifact().GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS {
			return sa.GetPath(), nil
		}
	}
	return stored.GetPath(), nil
}

// Renders and launches a seat: the process on loopback, a guard listener on the mesh address in
// front of it when other seats connect, or the process on the mesh address itself when exposed
func (m *Manager) launchSeat(ctx context.Context, p *prepared) (*v1.Instance, *v1.Task, error) {
	seat := proto.Clone(p.seat).(*v1.SeatSpec)
	rt, install, name := p.rt, p.install, p.name
	host := bindHost
	var err error
	var localPort int
	if seat.GetExposed() {
		if seat.GetAddress() == "" {
			return nil, nil, fmt.Errorf("%w: an exposed seat needs the mesh address to bind", runtimes.ErrParam)
		}
		host = hostOf(seat.GetAddress())
		if localPort, err = freePortOn(host); err != nil {
			return nil, nil, err
		}
		seat.Port = uint32(localPort)
	} else {
		if localPort, err = freePort(); err != nil {
			return nil, nil, err
		}
		seat.Port = uint32(localPort)
	}
	seat.LocalPort = uint32(localPort)
	var guard *forwarder
	if (p.role.Listens || p.role.Rendezvous) && !seat.GetExposed() {
		if seat.GetAddress() == "" {
			return nil, nil, fmt.Errorf("%w: a seat other seats connect to needs the mesh address for its guard listener", runtimes.ErrParam)
		}
		guard, err = newForwarder(net.JoinHostPort(hostOf(seat.GetAddress()), "0"), net.JoinHostPort(bindHost, strconv.Itoa(localPort)), seat.GetAdmit(), p.role.Single, name, m.Log)
		if err != nil {
			return nil, nil, err
		}
		seat.Port = uint32(guard.Port())
	}
	fail := func(err error) (*v1.Instance, *v1.Task, error) {
		if guard != nil {
			guard.Close()
		}
		return nil, nil, err
	}
	draft, err := m.draftPath(seat.GetDraft())
	if err != nil {
		return fail(err)
	}
	artifacts := map[string]string{}
	var unlock func()
	if p.stored != nil {
		key := store.Key(p.stored.GetSourceId(), p.stored.GetRepo(), p.stored.GetGroup())
		unlock = m.Store.Lock(key)
		touched, err := m.Store.Touch(p.stored.GetSourceId(), p.stored.GetRepo(), p.stored.GetGroup())
		if err != nil {
			unlock()
			return fail(fmt.Errorf("%s %s is no longer stored, pull it again: %w", p.stored.GetRepo(), p.stored.GetGroup(), err))
		}
		m.Events.Publish(v1.EventKind_EVENT_KIND_MODEL, v1.EventAction_EVENT_ACTION_UPDATED, key, touched)
		artifacts = m.artifacts(p.stored, rt)
	}
	if unlock != nil {
		defer unlock()
	}
	input := runtimes.Launch{
		Name:          name,
		Params:        p.params,
		Artifacts:     artifacts,
		Host:          host,
		Port:          localPort,
		Install:       runtimes.Install{Path: install.GetPath(), Dir: install.GetDir(), Version: install.GetVersion()},
		Devices:       seatDevices(p.profile, seat.GetDevices()),
		Descriptor:    p.descriptor,
		Plan:          p.plan,
		Stored:        p.companions,
		Model:         p.stored,
		Force:         p.req.GetForce(),
		Seat:          seat,
		InstallRecord: install,
		Draft:         draft,
	}
	var prep *runtimes.Command
	if p.stored != nil && rt.Prepares(p.stored.GetFormatId()) {
		if artifacts["prepared_dir"] == "" {
			return fail(fmt.Errorf("%s needs a prepared tree for %s and the store has none", rt.ID(), p.stored.GetGroup()))
		}
		if prep, err = rt.Prepare(input); err != nil {
			return fail(err)
		}
		launchArtifacts := make(map[string]string, len(artifacts))
		for k, v := range artifacts {
			launchArtifacts[k] = v
		}
		launchArtifacts["weights_dir"] = artifacts["prepared_dir"]
		input.Artifacts = launchArtifacts
	}
	rendered, err := rt.LaunchSeat(input)
	if err != nil {
		return fail(err)
	}
	health, err := runtimes.SeatHealth(rt, seat)
	if err != nil {
		return fail(err)
	}
	endpoint := fmt.Sprintf("http://%s:%d", host, localPort)
	if health.Kind != runtimes.HealthHTTP {
		endpoint = fmt.Sprintf("tcp://%s", net.JoinHostPort(hostOf(seat.GetAddress()), strconv.Itoa(int(seat.GetPort()))))
	}
	rec := &v1.Instance{
		Id:             db.NewID(),
		Name:           name,
		SourceId:       p.req.GetSourceId(),
		Repo:           p.req.GetRepo(),
		Group:          p.req.GetGroup(),
		RuntimeId:      rt.ID(),
		InstallId:      install.GetId(),
		Params:         rendered.Params,
		Command:        append([]string{rendered.Command}, rendered.Args...),
		Endpoint:       endpoint,
		State:          v1.InstanceState_INSTANCE_STATE_STARTING,
		CreatedAt:      timestamppb.Now(),
		Plan:           p.plan,
		Request:        proto.Clone(p.req).(*v1.RunRequest),
		DesiredRunning: true,
		Seat:           seat,
	}
	rec.Request.Seat = seat
	in := m.newInstance(rec, rt)
	in.guard = guard
	m.mu.Lock()
	if m.conflictLocked(name, "") {
		m.mu.Unlock()
		return fail(fmt.Errorf("%w: instance %q is already running", runtimes.ErrParam, name))
	}
	m.list = append(m.list, in)
	m.pruneLocked()
	m.mu.Unlock()
	m.Events.Publish(v1.EventKind_EVENT_KIND_INSTANCE, v1.EventAction_EVENT_ACTION_CREATED, rec.GetId(), in.snapshot())
	prepDir := artifacts["prepared_dir"]
	prepTimeout := rt.PrepareTimeout()
	labels := map[string]string{"instance": rec.GetId(), "name": name, "formation": seat.GetFormationId(), "role": seat.GetRole()}
	task := m.Tasks.Start(kindRun, "seat "+name, labels, func(ctx context.Context, h *tasks.Handle) error {
		if prep != nil {
			if err := m.runPrepare(ctx, h, in, prep, install.GetDir(), prepDir, prepTimeout, p.req.GetForce()); err != nil {
				return err
			}
		}
		return m.startSeat(ctx, h, in, rendered, install)
	})
	in.update(func(r *v1.Instance) { r.TaskId = task.GetId() })
	return in.snapshot(), task, nil
}

// Launches the seat's process and waits by the role's health
func (m *Manager) startSeat(ctx context.Context, h *tasks.Handle, in *instance, rendered *runtimes.Command, install *v1.Install) error {
	rec := in.snapshot()
	h.Logf("command: %s", strings.Join(rec.GetCommand(), " "))
	if g := in.guardOf(); g != nil {
		h.Logf("guard listener %s forwards to %s:%d, admitting %s", g.Addr(), bindHost, rec.GetSeat().GetLocalPort(), strings.Join(rec.GetSeat().GetAdmit(), ", "))
	} else if rec.GetSeat().GetExposed() {
		h.Logf("seat exposed on %s:%d", rec.GetSeat().GetAddress(), rec.GetSeat().GetPort())
	}
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
		h.Logf("stop requested before the seat was ready")
		proc.Stop(in.grace())
		return errStopped
	}
	if err := m.waitSeatReady(ctx, h, in, proc); err != nil {
		return err
	}
	proc.Sync()
	lines := proc.Log().Tail(0)
	measurements := in.rt.Measure(lines)
	transport := in.rt.Transport(lines)
	in.update(func(r *v1.Instance) { r.Transport = transport })
	if transport != "" {
		h.Logf("transport %s", transport)
	} else {
		go m.watchTransport(in)
	}
	var probe *v1.TemplateProbe
	if role, err := runtimes.RoleOf(in.rt, rec.GetSeat()); err == nil && role.Head {
		probe = m.probeTemplate(ctx, h.Logf, in)
	}
	m.ready(h, in, measurements, probe)
	return nil
}

// Waits for a seat by its role's health: an HTTP path, a hello exchange, or the process alone
func (m *Manager) waitSeatReady(ctx context.Context, h *tasks.Handle, in *instance, proc launch.Handle) error {
	rec := in.snapshot()
	health, err := runtimes.SeatHealth(in.rt, rec.GetSeat())
	if err != nil {
		return err
	}
	interval, timeout := health.Interval, health.Timeout
	if interval <= 0 {
		interval = time.Second
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	switch health.Kind {
	case runtimes.HealthHTTP:
		url := rec.GetEndpoint() + health.Path
		h.Message("waiting for " + url)
		err = launch.WaitHealthy(ctx, proc, url, interval, timeout)
	case runtimes.HealthHello:
		addr := net.JoinHostPort(bindHost, strconv.Itoa(int(rec.GetSeat().GetLocalPort())))
		if rec.GetSeat().GetExposed() {
			addr = net.JoinHostPort(hostOf(rec.GetSeat().GetAddress()), strconv.Itoa(int(rec.GetSeat().GetPort())))
		}
		h.Message("waiting for a hello from " + addr)
		err = waitHello(ctx, proc, addr, interval, timeout, func(version string) { h.Logf("rpc server speaks protocol %s", version) })
	case runtimes.HealthProcess:
		h.Message("waiting for the process to settle")
		err = waitProcess(ctx, proc, processSettle)
	}
	if err == nil {
		return nil
	}
	if in.stopRequested() {
		h.Logf("stop requested before the seat was ready")
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

// Polls a ggml rpc server with the hello command until it answers or the process exits
func waitHello(ctx context.Context, proc launch.Handle, addr string, interval, timeout time.Duration, spoke func(string)) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		version, err := rpcHello(ctx, addr)
		if err == nil {
			spoke(version)
			return nil
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("no hello within %s: %w", timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-proc.Done():
			if err := proc.Err(); err != nil {
				return fmt.Errorf("exited before answering a hello: %w", err)
			}
			return errors.New("exited before answering a hello")
		case <-time.After(interval):
		}
	}
}

// Waits for a process known by its liveness alone to run for the settle time
func waitProcess(ctx context.Context, proc launch.Handle, settle time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-proc.Done():
		if err := proc.Err(); err != nil {
			return fmt.Errorf("exited: %w", err)
		}
		return errors.New("exited")
	case <-time.After(settle):
		return nil
	}
}

// Reads the seat's log for the transport its collective or rpc server took, until a line says
func (m *Manager) watchTransport(in *instance) {
	deadline := time.Now().Add(transportWindow)
	for time.Now().Before(deadline) {
		select {
		case <-in.exited:
			return
		case <-time.After(transportPoll):
		}
		proc := in.handle()
		if proc == nil {
			return
		}
		proc.Sync()
		if t := in.rt.Transport(proc.Log().Tail(0)); t != "" {
			in.update(func(r *v1.Instance) { r.Transport = t })
			return
		}
	}
}

// Reads the transport line from the seat's log, recording it when one has appeared
func (in *instance) readTransport() string {
	proc := in.handle()
	if proc != nil {
		proc.Sync()
		if t := in.rt.Transport(proc.Log().Tail(0)); t != "" {
			in.update(func(r *v1.Instance) { r.Transport = t })
			return t
		}
	}
	return in.snapshot().GetTransport()
}

func (in *instance) guardOf() *forwarder {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.guard
}

// Binds the guard listener again for a seat adopted after a restart, on the port peers know
func (m *Manager) restoreGuard(in *instance) {
	rec := in.snapshot()
	seat := rec.GetSeat()
	role, err := runtimes.RoleOf(in.rt, seat)
	if err != nil || !role.Listens || seat.GetExposed() {
		return
	}
	g, err := newForwarder(net.JoinHostPort(hostOf(seat.GetAddress()), strconv.Itoa(int(seat.GetPort()))), net.JoinHostPort(bindHost, strconv.Itoa(int(seat.GetLocalPort()))), seat.GetAdmit(), role.Single, rec.GetName(), m.Log)
	if err != nil {
		m.Log.Warn("guard listener not restored, the conductor will relaunch the seat", "name", rec.GetName(), "err", err)
		m.fail(in, fmt.Errorf("guard listener lost across the restart: %w", err))
		if proc := in.handle(); proc != nil {
			proc.Stop(in.grace())
		}
		return
	}
	in.mu.Lock()
	in.guard = g
	in.mu.Unlock()
}

// The measurement key of bytes a seat's guard listener forwarded to it
const GuardReceivedKey = "guard.received"

// Reads a seat's guard counter and its log's transport line into its record and returns the record
func (m *Manager) Refresh(id string) (*v1.Instance, error) {
	in, err := m.find(id)
	if err != nil {
		return nil, err
	}
	if g := in.guardOf(); g != nil {
		received := g.Received()
		in.update(func(r *v1.Instance) {
			r.Measurements = mergeMeasurements(r.Measurements, []*v1.Measurement{{Key: GuardReceivedKey, Bytes: received, Line: "bytes the head streamed through the guard listener"}})
		})
	}
	in.readTransport()
	return in.snapshot(), nil
}

// Live instances hosting seats of a formation on this node
func (m *Manager) SeatsOf(formationID string) []*v1.Instance {
	var out []*v1.Instance
	for _, rec := range m.List(true) {
		if rec.GetSeat().GetFormationId() == formationID {
			out = append(out, rec)
		}
	}
	return out
}

// Live instances hosting seats of formations a conductor owns
func (m *Manager) SeatsConductedBy(nodeID string) []*v1.Instance {
	var out []*v1.Instance
	for _, rec := range m.List(true) {
		if rec.GetSeat() != nil && rec.GetSeat().GetConductor() == nodeID {
			out = append(out, rec)
		}
	}
	return out
}

// Every seat this node hosts or hosted as its record advertises it, a finished one with its error
// and triage, so a failure reaches the conductor over the sync
func (m *Manager) SeatRefs() []*v1.SeatRef {
	var out []*v1.SeatRef
	for _, rec := range m.List(false) {
		s := rec.GetSeat()
		if s == nil {
			continue
		}
		out = append(out, &v1.SeatRef{FormationId: s.GetFormationId(), InstanceId: rec.GetId(), Role: s.GetRole(), Rank: s.GetRank(), State: rec.GetState(), Transport: rec.GetTransport(), Error: rec.GetError(), Triage: rec.GetTriage()})
	}
	return out
}
