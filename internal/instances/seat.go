package instances

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/launch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
	"google.golang.org/protobuf/proto"
)

const (
	// A seat's log is read for its transport line this often, for this long after it is ready
	transportPoll   = 5 * time.Second
	transportWindow = 10 * time.Minute
	// A rank known by its process alone is ready once it has run this long
	processSettle = 3 * time.Second
)

// Resolves a seat launch from the runtime the conductor named, its install, role, files, params and planned devices
func (m *Manager) prepareSeat(ctx context.Context, req *v1.RunRequest, self string) (*prepared, error) {
	req = proto.Clone(req).(*v1.RunRequest)
	seat := req.GetSeat()
	if req.GetRuntimeId() == "" {
		return nil, fmt.Errorf("%w: a seat launch names its runtime", runtimes.ErrParam)
	}
	if seat.GetFormationId() == "" || seat.GetRole() == "" {
		return nil, fmt.Errorf("%w: a seat block names its formation and role", runtimes.ErrParam)
	}
	if req.GetSnapshot() {
		return nil, fmt.Errorf("%w: a seat follows its formation, snapshots are taken of solo runs", runtimes.ErrParam)
	}
	rt, err := m.Runtimes.Get(req.GetRuntimeId())
	if err != nil {
		return nil, err
	}
	role, err := runtimes.RoleOf(rt, seat)
	if err != nil {
		return nil, err
	}
	install, err := m.pinInstall(ctx, req, rt)
	if err != nil {
		return nil, err
	}
	name := req.GetName()
	if name == "" {
		name = fmt.Sprintf("%s/%s%d", seat.GetName(), role.Name, seat.GetRank())
		req.Name = name
	}
	if m.conflict(name, "", self) {
		return nil, fmt.Errorf("%w: instance %q is already running", runtimes.ErrParam, name)
	}
	profile, err := m.Host.Profile(ctx, true)
	if err != nil {
		return nil, err
	}
	if err := seatPlanned(profile, seat); err != nil {
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

// Checks that the devices and host pools a seat's plan names exist on this node
func seatPlanned(profile *v1.HostProfile, seat *v1.SeatSpec) error {
	ids, bytes := seat.GetDevices(), seat.GetDeviceBytes()
	for i, id := range ids {
		if strings.Contains(id, "/") {
			continue
		}
		if i >= len(bytes) {
			return fmt.Errorf("%w: the plan lists %d devices and %d byte shares", runtimes.ErrParam, len(ids), len(bytes))
		}
		if deviceIn(profile, id) == nil {
			return fmt.Errorf("%w: the plan names device %s, which this node does not hold", runtimes.ErrParam, id)
		}
	}
	for _, pu := range seat.GetMemory().GetPools() {
		if pu.GetKind() == v1.PoolKind_POOL_KIND_HOST && poolByID(profile, pu.GetPoolId()) == nil {
			return fmt.Errorf("%w: the plan keeps %s in host pool %s, which this node's profile does not list", runtimes.ErrParam, estimate.Human(pu.GetUsedBytes()), pu.GetPoolId())
		}
	}
	return nil
}

// The profile's device with an id
func deviceIn(profile *v1.HostProfile, id string) *v1.Device {
	for _, d := range profile.GetDevices() {
		if d.GetId() == id {
			return d
		}
	}
	return nil
}

// The profile's pool with an id
func poolByID(profile *v1.HostProfile, id string) *v1.MemoryPool {
	for _, p := range profile.GetPools() {
		if p.GetId() == id {
			return p
		}
	}
	return nil
}

// The bare devices the plan lists on this node, ids qualified by another node left to the runtime
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

// Binds one guard listener per rank on reserved mesh ports when other seats connect through one
func (m *Manager) seatGuards(in *instance, p *prepared, seat *v1.SeatSpec, ranks int) error {
	if !(p.role.Listens || p.role.Rendezvous) || seat.GetExposed() {
		return nil
	}
	if seat.GetAddress() == "" {
		return fmt.Errorf("%w: a seat other seats connect to needs the mesh address for its guard listener", runtimes.ErrParam)
	}
	guardHost := hostOf(seat.GetAddress())
	var guards []*forwarder
	fail := func(err error) error {
		for _, g := range guards {
			g.Close()
		}
		return err
	}
	for rank := range ranks {
		guardPort, err := m.reserve(in, guardHost, 1)
		if err != nil {
			return fail(err)
		}
		name := p.name
		if ranks > 1 {
			name = fmt.Sprintf("%s rank %d", p.name, rank)
		}
		m.yield(in)
		guard, err := newForwarder(net.JoinHostPort(guardHost, strconv.Itoa(guardPort)), "", seat.GetAdmit(), p.role.Single, name, m.Log)
		m.confirm(in)
		if err != nil {
			return fail(err)
		}
		guards = append(guards, guard)
	}
	seat.Port = uint32(guards[0].Port())
	in.mu.Lock()
	in.guards = guards
	in.mu.Unlock()
	return nil
}

// Renders and launches a seat on reserved ports behind a guard, or on the mesh address when exposed
func (m *Manager) launchSeat(ctx context.Context, h *tasks.Handle, in *instance, p *prepared) error {
	seat := proto.Clone(p.seat).(*v1.SeatSpec)
	rt, install, name := p.rt, p.install, p.name
	host := bindHost
	if seat.GetExposed() {
		if seat.GetAddress() == "" {
			return fmt.Errorf("%w: an exposed seat needs the mesh address to bind", runtimes.ErrParam)
		}
		host = hostOf(seat.GetAddress())
	}
	ranks := rt.Ranks(stringParams(p.params))
	if err := ranksServeHTTP(rt, seat, ranks); err != nil {
		return err
	}
	if err := m.seatGuards(in, p, seat, ranks); err != nil {
		return err
	}
	draft, err := m.draftPath(seat.GetDraft())
	if err != nil {
		return err
	}
	artifacts := map[string]string{}
	if p.stored != nil {
		key := store.Key(p.stored.GetSourceId(), p.stored.GetRepo(), p.stored.GetGroup())
		unlock := m.Store.Lock(key)
		touched, err := m.Store.Touch(p.stored.GetSourceId(), p.stored.GetRepo(), p.stored.GetGroup())
		unlock()
		if err != nil {
			return fmt.Errorf("%s %s is no longer stored, pull it again: %w", p.stored.GetRepo(), p.stored.GetGroup(), err)
		}
		m.Events.Publish(v1.EventKind_EVENT_KIND_MODEL, v1.EventAction_EVENT_ACTION_UPDATED, key, touched)
		artifacts = m.artifacts(p.stored, rt)
	}
	// Every seat answers to the formation's name, so one route model reaches each of them
	served := name
	if seat.GetName() != "" {
		served = seat.GetName()
	}
	input := p.input()
	input.Name, input.Artifacts, input.Host, input.Draft = served, artifacts, host, draft
	if p.stored != nil && rt.Prepares(p.stored.GetFormatId()) {
		if artifacts["prepared_dir"] == "" {
			return fmt.Errorf("%s needs a prepared tree for %s and the store has none", rt.ID(), p.stored.GetGroup())
		}
		input.Seat = seat
		prep, err := rt.Prepare(input)
		if err != nil {
			return err
		}
		if err := m.runPrepare(ctx, h, in, prep, install.GetDir(), artifacts["prepared_dir"], rt.PrepareTimeout()); err != nil {
			return err
		}
		launchArtifacts := maps.Clone(artifacts)
		launchArtifacts["weights_dir"] = artifacts["prepared_dir"]
		input.Artifacts = launchArtifacts
	}
	health, err := runtimes.SeatHealth(rt, seat)
	if err != nil {
		return err
	}
	in.setHealth(health)
	span := rt.PortSpan(input)
	var handle launch.Handle
	for try := 1; ; try++ {
		localPort, err := m.reserve(in, host, span)
		if err != nil {
			return err
		}
		seat.LocalPort = uint32(localPort)
		guards := in.guardsOf()
		if len(guards) == 0 {
			seat.Port = uint32(localPort)
		}
		input.Port, input.Seat = localPort, seat
		cmds, err := renderRanks(input, ranks, rt.LaunchSeat, func(in runtimes.Launch, cmd *runtimes.Command) error {
			// A helper seat such as an rpc server runs its own program, which the extra words are not for
			if p.role.Helper {
				return nil
			}
			extra, err := runtimes.RenderArgs(seat.GetArgs(), runtimes.ScopeOf(in, cmd))
			cmd.Args = append(cmd.Args, extra...)
			return err
		})
		if err != nil {
			m.release(in)
			return err
		}
		mesh := make([]uint32, len(cmds))
		for i, cmd := range cmds {
			mesh[i] = uint32(cmd.Port)
			if i < len(guards) {
				guards[i].Retarget(net.JoinHostPort(bindHost, strconv.Itoa(cmd.Port)))
				mesh[i] = uint32(guards[i].Port())
			}
		}
		rendered := cmds[0]
		endpoint := fmt.Sprintf("http://%s", hostPort(host, localPort))
		if health.Kind != runtimes.HealthHTTP {
			endpoint = fmt.Sprintf("tcp://%s", net.JoinHostPort(hostOf(seat.GetAddress()), strconv.Itoa(int(seat.GetPort()))))
		}
		inventory, err := rt.Inventory(input, rendered.Params)
		if err != nil {
			m.release(in)
			return err
		}
		recorded := proto.Clone(seat).(*v1.SeatSpec)
		in.update(func(r *v1.Instance) {
			r.Params = rendered.Params
			r.Command = auth.RedactEnv(append([]string{rendered.Command}, rendered.Args...), rendered.Env)
			r.Endpoint, r.InstallId, r.Seat, r.Inventory = endpoint, install.GetId(), recorded, inventory
			r.Ranks = rankRecords(cmds, host, mesh)
			if r.Request != nil {
				r.Request.Seat = proto.Clone(recorded).(*v1.SeatSpec)
			}
		})
		for i, g := range guards {
			h.Logf("guard listener %s forwards to %s:%d, admitting %s", g.Addr(), bindHost, cmds[i].Port, strings.Join(seat.GetAdmit(), ", "))
		}
		if len(guards) == 0 && seat.GetExposed() {
			for _, cmd := range cmds {
				h.Logf("seat exposed on %s:%d", seat.GetAddress(), cmd.Port)
			}
		}
		m.yield(in)
		if handle, err = m.startRanks(ctx, h, in, cmds, install.GetDir()); err != nil {
			m.release(in)
			return err
		}
		err = m.waitSeatReady(ctx, h, in, handle)
		if err == nil {
			break
		}
		if cancelled(ctx, err) {
			return errStopped
		}
		if exitedNow(handle) && runtimes.PortTaken(rt, span, localPort, linesOf(handle)) {
			m.release(in)
			if try < maxPortTries {
				h.Logf("a port from %d was taken before %s bound it, launching again on another reservation, try %d of %d", localPort, rt.ID(), try+1, maxPortTries)
				continue
			}
			return fmt.Errorf("%s on %d reservations in a row: %w", rt.PortRule().Summary, maxPortTries, err)
		}
		m.release(in)
		err = m.launchError(in, err)
		logHits(h, in)
		return err
	}
	m.confirm(in)
	handle.Sync()
	lines := handle.Log().Tail(0)
	measurements := in.rt.Measure(lines)
	if picked := m.chosen(h.Logf, in, lines); len(picked) > 0 {
		if err := m.reinventory(in, p); err != nil {
			return m.abandon(h, in, handle, err)
		}
	}
	transport := in.rt.Transport(lines)
	in.update(func(r *v1.Instance) { r.Transport = transport })
	if transport != "" {
		h.Logf("transport %s", transport)
	} else {
		go m.watchTransport(in)
	}
	var tpl *v1.ChatTemplate
	if p.role.Head {
		if tpl, err = m.readTemplate(ctx, h.Logf, in); err != nil {
			return m.abandon(h, in, handle, err)
		}
	}
	m.ready(h, in, p.generation, measurements, tpl)
	return nil
}

// Waits for a seat by its role's health, an HTTP path, a hello, or its process, stopping it otherwise
func (m *Manager) waitSeatReady(ctx context.Context, h *tasks.Handle, in *instance, proc launch.Handle) error {
	rec := in.snapshot()
	health := in.healthOf()
	var err error
	interval, slowAfter := health.Interval, health.SlowAfter
	if slowAfter <= 0 {
		slowAfter = 5 * time.Minute
	}
	slow := func(what string) func(time.Duration) {
		return func(waited time.Duration) {
			h.Logf("%s has not %s after %s, waiting for as long as the process lives", rec.GetName(), what, waited)
			m.Log.Warn("seat slow to become ready", "name", rec.GetName(), "waited", waited)
		}
	}
	switch health.Kind {
	case runtimes.HealthHTTP:
		for _, ref := range rankRefs(rec) {
			url := ref.endpoint + health.Path
			h.Message("waiting for " + url)
			if err = launch.WaitHealthy(ctx, proc, url, interval, slowAfter, slow("answered "+url)); err != nil {
				break
			}
		}
	case runtimes.HealthHello:
		addr := net.JoinHostPort(bindHost, strconv.Itoa(int(rec.GetSeat().GetLocalPort())))
		if rec.GetSeat().GetExposed() {
			addr = net.JoinHostPort(hostOf(rec.GetSeat().GetAddress()), strconv.Itoa(int(rec.GetSeat().GetPort())))
		}
		h.Message("waiting for a hello from " + addr)
		err = waitHello(ctx, proc, addr, interval, slowAfter, slow("answered a hello"), func(version string) { h.Logf("rpc server speaks protocol %s", version) })
	case runtimes.HealthProcess:
		h.Message("waiting for the process to settle")
		err = waitProcess(ctx, proc, processSettle)
	}
	if err != nil && ctx.Err() != nil {
		h.Logf("stop requested before the seat was ready")
		in.stopProcess(proc, watchExit(proc), ReasonStopRequested)
		return errStopped
	}
	if err != nil && !exitedNow(proc) {
		in.stopProcess(proc, watchExit(proc), ReasonLaunchFailed)
		in.recordDrainCrash()
	}
	return err
}

// Polls a ggml rpc server with the hello command until it answers or the process exits
func waitHello(ctx context.Context, proc launch.Handle, addr string, interval, slowAfter time.Duration, slow func(time.Duration), spoke func(string)) error {
	start := time.Now()
	warn := time.NewTicker(slowAfter)
	defer warn.Stop()
	for {
		version, err := rpcHello(ctx, addr)
		if err == nil {
			spoke(version)
			return nil
		}
		select {
		case <-warn.C:
			slow(time.Since(start).Round(time.Second))
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
		in.mu.Lock()
		exited := in.exited
		in.mu.Unlock()
		select {
		case <-exited:
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

// The guard listeners of every rank, rank 0 first
func (in *instance) guardsOf() []*forwarder {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]*forwarder(nil), in.guards...)
}

// Rank 0's guard listener, nil for a seat reached without one
func (in *instance) guardOf() *forwarder {
	if guards := in.guardsOf(); len(guards) > 0 {
		return guards[0]
	}
	return nil
}

// Rebinds an adopted seat's guards on the ports its peers know, one per rank, failing when a port is gone
func (m *Manager) restoreGuard(in *instance) error {
	rec := in.snapshot()
	seat := rec.GetSeat()
	role, err := runtimes.RoleOf(in.rt, seat)
	if err != nil {
		return err
	}
	if !role.Listens || seat.GetExposed() {
		return nil
	}
	refs := rankRefs(rec)
	var guards []*forwarder
	for _, ref := range refs {
		local := int(seat.GetLocalPort())
		name := rec.GetName()
		if len(refs) > 1 {
			if local, err = endpointPort(ref.endpoint); err != nil {
				err = fmt.Errorf("rank %d endpoint %s: %w", ref.rank, ref.endpoint, err)
			}
			name = fmt.Sprintf("%s rank %d", rec.GetName(), ref.rank)
		}
		var g *forwarder
		if err == nil {
			g, err = newForwarder(net.JoinHostPort(hostOf(seat.GetAddress()), strconv.Itoa(int(ref.port))), net.JoinHostPort(bindHost, strconv.Itoa(local)), seat.GetAdmit(), role.Single, name, m.Log)
		}
		if err != nil {
			for _, g := range guards {
				g.Close()
			}
			return fmt.Errorf("guard listener lost across the restart: %w", err)
		}
		guards = append(guards, g)
	}
	in.mu.Lock()
	in.guards = guards
	in.mu.Unlock()
	return nil
}

// Adopts a live seat whose guard returns on its known port, then verifies it in a task by role health
func (m *Manager) adoptSeat(ctx context.Context, in *instance, proc launch.Handle) {
	rec := in.snapshot()
	since := time.Now()
	in.update(func(r *v1.Instance) {
		r.State, r.ReadyAt = v1.InstanceState_INSTANCE_STATE_STARTING, nil
	})
	m.Log.Info("adopted seat, verifying it", "name", rec.GetName(), "pid", rec.GetPid(), "formation", rec.GetSeat().GetFormationId())
	first := in.startAttempt("adopt "+rec.GetName(), func(ctx context.Context, h *tasks.Handle) error {
		h.Logf("adopted pid %d left running by the previous daemon", rec.GetPid())
		if err := m.restoreGuard(in); err != nil {
			return m.abandon(h, in, proc, err)
		}
		role, err := runtimes.RoleOf(in.rt, rec.GetSeat())
		if err != nil {
			return m.abandon(h, in, proc, err)
		}
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
		var tpl *v1.ChatTemplate
		if role.Head {
			if tpl, err = m.readTemplate(ctx, h.Logf, in); err != nil {
				return m.abandon(h, in, proc, err)
			}
		}
		m.ready(h, in, rec.GetGeneration(), in.rt.Measure(lines), tpl)
		return nil
	})
	in.startLoop(first, false)
}

// Verifies an adopted seat by its role's health, a hello to an idle stage, or the conductor's head record
func (m *Manager) verifyAdopted(ctx context.Context, h *tasks.Handle, in *instance, proc launch.Handle, since time.Time) error {
	rec := in.snapshot()
	health, err := runtimes.SeatHealth(in.rt, rec.GetSeat())
	if err != nil {
		return m.abandon(h, in, proc, err)
	}
	in.setHealth(health)
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
			return m.abandon(h, in, proc, fmt.Errorf("no hello from the adopted stage at %s: %w", addr, err))
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
		return m.abandon(h, in, proc, errors.New(holder+", and this node has no conductor record to confirm the head by"))
	}
	cctx, cancel := context.WithTimeout(ctx, adoptConfirmWindow)
	defer cancel()
	if err := m.Formations.HeadReady(cctx, seat.GetFormationId(), since); err != nil {
		return m.abandon(h, in, proc, fmt.Errorf("%s, and the conductor did not confirm the head on it: %w", holder, err))
	}
	h.Logf("the conductor's record confirms the head is ready on this stage")
	return nil
}

// The measurement key of bytes a seat's guard listener forwarded to it
const GuardReceivedKey = "guard.received"

// Reads a seat's guard counter and transport line into its record, writing only when either moved, and returns the record
func (m *Manager) Refresh(id string) (*v1.Instance, error) {
	in, err := m.find(id)
	if err != nil {
		return nil, err
	}
	if guards := in.guardsOf(); len(guards) > 0 {
		var received uint64
		for _, g := range guards {
			received += g.Received()
		}
		in.update(func(r *v1.Instance) {
			if measured(r, GuardReceivedKey) != received {
				r.Measurements = mergeMeasurements(r.Measurements, []*v1.Measurement{{Key: GuardReceivedKey, Bytes: received, Line: "bytes the head streamed through the guard listener"}})
			}
		})
	}
	in.readTransport()
	return in.snapshot(), nil
}

// The bytes a record measures under a key, zero when it has none
func measured(rec *v1.Instance, key string) uint64 {
	for _, ms := range rec.GetMeasurements() {
		if ms.GetKey() == key {
			return ms.GetBytes()
		}
	}
	return 0
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

// Every seat this node hosts or hosted with its error and triage, for the conductor over the sync
func (m *Manager) SeatRefs() []*v1.SeatRef {
	var out []*v1.SeatRef
	for _, rec := range m.List(false) {
		s := rec.GetSeat()
		if s == nil {
			continue
		}
		out = append(out, &v1.SeatRef{FormationId: s.GetFormationId(), InstanceId: rec.GetId(), Role: s.GetRole(), Rank: s.GetRank(), State: rec.GetState(), Transport: rec.GetTransport(), Error: rec.GetError(), Triage: rec.GetTriage(), Conditions: rec.GetConditions(), Ranks: rec.GetRanks()})
	}
	return out
}
