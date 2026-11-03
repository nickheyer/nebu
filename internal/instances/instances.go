// Package instances runs, supervises, records, recovers, and publishes models
package instances

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/calibrate"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/launch"
	"github.com/nickheyer/nebu/pkg/proc"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/snapshot"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/text"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	kindRun      = "run"
	historyMax   = 100
	bindHost     = "127.0.0.1"
	closeTimeout = 30 * time.Second
	probeTimeout = 2 * time.Second
	// Timeout for reading a diffusion runtime's capabilities
	modesTimeout = 2 * time.Minute
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
	// Extra words for the server command, templates rendered at launch
	Args string
}

func (r *Reservation) placement() v1.Placement {
	if r == nil {
		return v1.Placement_PLACEMENT_UNSPECIFIED
	}
	return r.Placement
}

// The slot's extra words, none outside a slot
func (r *Reservation) args() string {
	if r == nil {
		return ""
	}
	return r.Args
}

type swapKey struct{}

// Marks a context as a swap, allowed beside an occupant
func WithSwap(ctx context.Context) context.Context { return context.WithValue(ctx, swapKey{}, true) }

// Whether the context marks a swap, so a replacement may launch beside the occupant it replaces
func FromSwap(ctx context.Context) bool { v, _ := ctx.Value(swapKey{}).(bool); return v }

// Slot manager interface for bound instances
type Slots interface {
	Reservation(ctx context.Context, slotID string) (*Reservation, error)
	// Whether a slot answers to the name, as its own name or an alias
	Owns(name string) bool
	// Called after every state change, outside the instance lock
	OnInstance(*v1.Instance)
}

// The conductor's record a stage adopted while a head holds it relies on as evidence of that head
type Formations interface {
	// Waits for a conductor record newer than since saying the head is ready, or returns why not
	HeadReady(ctx context.Context, formationID string, since time.Time) error
}

// An adopted stage a head holds waits this long for the conductor's record
const adoptConfirmWindow = 2 * time.Minute

// Manages daemon instances
type Manager struct {
	DB  *db.DB
	Dir string
	// The mesh port range seats and guards bind in, nil or false when any port will do
	MeshPorts   func() (int, int, bool)
	Store       *store.Store
	Runtimes    *runtimes.Registry
	Installs    *installs.Manager
	Inspector   *inspect.Inspector
	Launcher    launch.Launcher
	Tasks       *tasks.Manager
	Host        *host.Prober
	Calibration *calibrate.Table
	// Publishes endpoints and names routes for the gateway
	Registrar pools.Registrar
	// Requests the gateway has in flight on an instance, read while draining and idling
	InFlight func(instanceID string) int
	// Requests the gateway has sent an instance since the daemon started, read while idling
	Dispatched func(instanceID string) uint64
	// Running and queued requests the scrape of an instance reports, read while idling, false when none was scraped
	Load func(instanceID string) (running, queued int, scraped bool)
	// Reads the TCP ports a launched process tree listens on, proc.ListeningPorts unless a test reads them another way
	Listening func(pid int) ([]int, error)
	// Reads the pids of a launched tree holding an NVIDIA device, the kernel's fd table unless a test names them another way
	CUDAPIDs func(root int) ([]int, error)
	// Longest wait for requests in flight before a process stops
	DrainTimeout time.Duration
	Events       *events.Bus
	// Set by the daemon once slots exist, nil until then
	Slots Slots
	// Set by the daemon once the conductor exists, nil until then
	Formations Formations
	// Set by the daemon, called as each runtime exits so the host is probed again
	Probe func()
	Log   *slog.Logger

	mu   sync.Mutex
	list []*instance
	// Whether the daemon is stopping every instance on its way out
	closing bool
	// Port reservation books by host
	books map[string]*proc.Reservations
}

// Resolved launch inputs
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
	// Slot defaults under request params, stored groups resolved to files, what the plan was made from
	layered map[string]string
	// What the plan left uncounted, logged when the run starts
	warning string
	// Other stored models available as companion parts
	companions []*v1.StoredModel
	// The seat block and its role, for a seat of a formation
	seat *v1.SeatSpec
	role runtimes.Role
	// Takes and restores the process image once ready, nil when the request asks for no snapshot
	snapshot *snapshot.Snapshotter
	// The request generation these inputs were resolved for
	generation uint64
}

// Plans a run without launching it
func (m *Manager) Plan(ctx context.Context, req *v1.RunRequest) (*v1.MemoryPlan, error) {
	p, err := m.prepare(ctx, req, "")
	if err != nil {
		return nil, err
	}
	if p.plan == nil {
		return &v1.MemoryPlan{Verdict: v1.FitVerdict_FIT_VERDICT_FITS, Detail: "runtime has no estimate policy"}, nil
	}
	return p.plan, nil
}

// Plans a stored model, records it as desired, and starts its reconciler, returning the record and the first launch task
func (m *Manager) Run(ctx context.Context, req *v1.RunRequest) (*v1.Instance, *v1.Task, error) {
	p, err := m.prepare(ctx, req, "")
	if err != nil {
		return nil, nil, err
	}
	if p.res != nil && p.res.InstanceID != "" && !FromSwap(ctx) {
		if cur, err := m.Get(p.res.InstanceID); err == nil && !Terminal(cur.GetState()) {
			return nil, nil, fmt.Errorf("%w: slot %s serves %s, swap instead", runtimes.ErrParam, p.res.Name, cur.GetName())
		}
	}
	if p.res != nil && p.res.FormationID != "" && !FromSwap(ctx) {
		return nil, nil, fmt.Errorf("%w: slot %s serves formation %s, swap instead", runtimes.ErrParam, p.res.Name, p.res.FormationName)
	}
	if p.req.GetIdleUnloadSeconds() > 0 && p.seat != nil {
		return nil, nil, fmt.Errorf("%w: a seat follows its formation and never unloads on its own", runtimes.ErrParam)
	}
	return m.create(ctx, p)
}

// Records a prepared run, names its route, and starts the reconciler on its first launch
func (m *Manager) create(ctx context.Context, p *prepared) (*v1.Instance, *v1.Task, error) {
	p.generation = 1
	rec := &v1.Instance{
		Id:             db.NewID(),
		Name:           p.name,
		SourceId:       p.req.GetSourceId(),
		Repo:           p.req.GetRepo(),
		Group:          p.req.GetGroup(),
		RuntimeId:      p.rt.ID(),
		InstallId:      p.install.GetId(),
		State:          v1.InstanceState_INSTANCE_STATE_STARTING,
		CreatedAt:      timestamppb.Now(),
		Plan:           p.plan,
		Request:        proto.Clone(p.req).(*v1.RunRequest),
		DesiredRunning: true,
		SlotId:         p.req.GetSlotId(),
		Seat:           p.seat,
		Generation:     1,
	}
	if p.stored != nil {
		rec.SourceId, rec.Repo, rec.Group = p.stored.GetSourceId(), p.stored.GetRepo(), p.stored.GetGroup()
	}
	rec.Conditions = SetCondition(rec.Conditions, CondScheduled, true, ReasonPlanned, planDetail(p), 1)
	rec.Conditions = SetCondition(rec.Conditions, CondLaunched, false, ReasonLaunching, "", 1)
	rec.Conditions = SetCondition(rec.Conditions, CondReady, false, ReasonStarting, "", 1)
	in := m.newInstance(rec, p.rt)
	in.prepared = p
	m.mu.Lock()
	if m.conflictLocked(p.name, p.req.GetSlotId(), "") {
		m.mu.Unlock()
		return nil, nil, fmt.Errorf("%w: instance %q is already running", runtimes.ErrParam, p.name)
	}
	m.list = append(m.list, in)
	m.pruneLocked()
	m.mu.Unlock()
	if err := m.DB.PutInstance(context.Background(), in.snapshot()); err != nil {
		m.Log.Warn("instance record write failed", "id", rec.GetId(), "err", err)
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_INSTANCE, v1.EventAction_EVENT_ACTION_CREATED, rec.GetId(), in.snapshot())
	if err := m.register(ctx, in, stringParams(p.params)); err != nil {
		in.update(func(r *v1.Instance) {
			r.State, r.Error, r.StoppedAt, r.DesiredRunning = v1.InstanceState_INSTANCE_STATE_FAILED, err.Error(), timestamppb.Now(), false
			r.Conditions = SetCondition(r.Conditions, CondReady, false, ReasonLaunchFailed, err.Error(), r.GetGeneration())
		})
		return nil, nil, err
	}
	first := in.launchAttempt("run "+p.name, p)
	in.startLoop(first, false)
	return in.snapshot(), first.task, nil
}

// A one line account of the plan a run was scheduled with
func planDetail(p *prepared) string {
	if p.plan == nil {
		return "the runtime has no estimate policy"
	}
	return text.Enum(p.plan.GetVerdict()) + ": " + p.plan.GetDetail()
}

// Replaces an instance's desired request, the reconciler draining the old process and launching the new
func (m *Manager) Apply(ctx context.Context, id string, req *v1.RunRequest) (*v1.Instance, error) {
	in, err := m.find(id)
	if err != nil {
		return nil, err
	}
	rec := in.snapshot()
	if Terminal(rec.GetState()) {
		return nil, fmt.Errorf("%w: %s is %s, run it again instead", runtimes.ErrParam, rec.GetName(), text.Enum(rec.GetState()))
	}
	if rec.GetSeat() != nil || req.GetSeat() != nil {
		return nil, fmt.Errorf("%w: a seat's request belongs to its formation", runtimes.ErrParam)
	}
	next := proto.Clone(req).(*v1.RunRequest)
	next.SlotId, next.Name = rec.GetSlotId(), rec.GetName()
	if next.GetInstallId() == "" && (next.GetRuntimeId() == "" || next.GetRuntimeId() == rec.GetRuntimeId()) {
		next.InstallId = rec.GetInstallId()
	}
	p, err := m.prepare(ctx, next, rec.GetId())
	if err != nil {
		return nil, err
	}
	var gen uint64
	in.update(func(r *v1.Instance) {
		r.Generation++
		gen = r.Generation
		r.Request = proto.Clone(p.req).(*v1.RunRequest)
		r.InstallId, r.RuntimeId, r.Plan = p.install.GetId(), p.rt.ID(), p.plan
		r.Conditions = SetCondition(r.Conditions, CondScheduled, true, ReasonPlanned, planDetail(p), gen)
	})
	p.generation = gen
	in.mu.Lock()
	in.next = p
	in.mu.Unlock()
	in.kick()
	return in.snapshot(), nil
}

// Resolves model, runtime, install, reservation, and plan, the instance named by self excluded from name checks
func (m *Manager) prepare(ctx context.Context, req *v1.RunRequest, self string) (*prepared, error) {
	if req.GetSeat() != nil {
		return m.prepareSeat(ctx, req, self)
	}
	p, err := m.prepareInputs(ctx, req, self)
	if err != nil {
		return nil, err
	}
	rt, params := p.rt, p.params
	if p.req.GetSnapshot() {
		if p.snapshot, err = m.snapshotter(p.planned, p.res.placement()); err != nil {
			return nil, err
		}
	}
	if rt.Policy() != nil {
		if p.plan, p.warning, err = m.Inspector.Plan(ctx, rt, p.descriptor, p.planned, p.layered, true, p.res.placement(), p.stored.GetRepo(), p.stored, p.companions); err != nil {
			return nil, err
		}
		for k, v := range p.plan.GetParams() {
			if _, deferred := rt.Policy().Deferred[k]; params.IsAuto(k) && !deferred {
				params[k] = v
			}
		}
	}
	return p, nil
}

// Resolves a solo run's model, runtime, install, reservation and params short of its plan
func (m *Manager) prepareInputs(ctx context.Context, req *v1.RunRequest, self string) (*prepared, error) {
	req = proto.Clone(req).(*v1.RunRequest)
	if err := runtimes.CheckArgs(req.GetArgs()); err != nil {
		return nil, err
	}
	if req.GetIdleUnloadSeconds() > 0 && (m.InFlight == nil || m.Dispatched == nil || m.Load == nil) {
		return nil, fmt.Errorf("%w: idle unload reads the gateway's in flight and dispatch counters and the scraped load, and this daemon wires none of them", runtimes.ErrParam)
	}
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
	if res != nil {
		if err := pinnable(profile, res.DeviceIDs); err != nil {
			return nil, err
		}
	}
	// Use the slot's runtime or the first compatible runtime
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
	install, err := m.pinInstall(ctx, req, rt)
	if err != nil {
		return nil, err
	}
	name := req.GetName()
	if name == "" {
		name = path.Base(stored.GetRepo()) + ":" + stored.GetGroup()
	}
	if m.conflict(name, req.GetSlotId(), self) {
		return nil, fmt.Errorf("%w: instance %q is already running", runtimes.ErrParam, name)
	}
	if req.GetSlotId() == "" && m.Slots != nil && m.Slots.Owns(name) {
		return nil, fmt.Errorf("%w: %q is a slot, run with the slot or pick another name", runtimes.ErrParam, name)
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
	// Request params override slot defaults, and stored groups named by path params resolve to their files
	layered, err := runtimes.ResolveStore(rt, runtimes.Merge(slotParams, req.GetParams()), append(append([]*v1.StoredModel{}, companions...), stored))
	if err != nil {
		return nil, err
	}
	params, err := runtimes.Resolve(rt, layered)
	if err != nil {
		return nil, err
	}
	p := &prepared{req: req, stored: stored, rt: rt, install: install, name: name, descriptor: descriptor, profile: profile, planned: planProfile, res: res, params: params, layered: layered}
	p.companions = companions
	return p, nil
}

// The launch input a prepared run renders and reports its inventory from, before its host, port and files are bound
func (p *prepared) input() runtimes.Launch {
	in := runtimes.Launch{
		Name:          p.name,
		Params:        p.params,
		Install:       runtimes.Install{Path: p.install.GetPath(), Dir: p.install.GetDir(), Version: p.install.GetVersion()},
		Descriptor:    p.descriptor,
		Plan:          p.plan,
		Profile:       p.planned,
		Stored:        p.companions,
		Model:         p.stored,
		InstallRecord: p.install,
	}
	if p.seat != nil {
		in.Devices = seatDevices(p.profile, p.seat.GetDevices())
		return in
	}
	in.Devices, in.Placement = slotDevices(p.planned, p.res), p.res.placement()
	return in
}

// What a recorded launch reports, its inputs resolved as its launch resolved them over its recorded plan and params
func (m *Manager) recordedInventory(ctx context.Context, rec *v1.Instance) (*v1.RuntimeInventory, error) {
	var p *prepared
	var err error
	if rec.GetSeat() != nil {
		p, err = m.prepareSeat(ctx, rec.GetRequest(), rec.GetId())
	} else if p, err = m.prepareInputs(ctx, rec.GetRequest(), rec.GetId()); err == nil {
		p.plan = rec.GetPlan()
	}
	if err != nil {
		return nil, err
	}
	return p.rt.Inventory(p.input(), rec.GetParams())
}

// Resolves the install a request names, or the runtime's default, and pins its id into the request
func (m *Manager) pinInstall(ctx context.Context, req *v1.RunRequest, rt runtimes.Runtime) (*v1.Install, error) {
	var install *v1.Install
	var err error
	if req.GetInstallId() != "" {
		install, err = m.Installs.Get(ctx, req.GetInstallId())
	} else {
		install, err = m.Installs.Default(ctx, rt.ID())
	}
	if err != nil {
		return nil, err
	}
	if install.GetRuntimeId() != rt.ID() {
		return nil, fmt.Errorf("%w: install %s is a %s install, not %s", runtimes.ErrParam, install.GetId(), install.GetRuntimeId(), rt.ID())
	}
	req.InstallId = install.GetId()
	return install, nil
}

// Stamps each process the host lists with the live instance whose process tree holds its pid
func (m *Manager) Attribute(profile *v1.HostProfile) {
	if len(profile.GetProcesses()) == 0 {
		return
	}
	type owner struct{ id, name string }
	roots := map[int]owner{}
	m.mu.Lock()
	for _, in := range m.list {
		in.mu.Lock()
		if !Terminal(in.rec.GetState()) {
			for _, ref := range rankRefs(in.rec) {
				if ref.pid > 0 {
					roots[int(ref.pid)] = owner{in.rec.GetId(), in.rec.GetName()}
				}
			}
		}
		in.mu.Unlock()
	}
	m.mu.Unlock()
	if len(roots) == 0 {
		return
	}
	pids := make([]int, 0, len(roots))
	for pid := range roots {
		pids = append(pids, pid)
	}
	owners := proc.Owners(pids)
	for _, pu := range profile.GetProcesses() {
		if root, ok := owners[int(pu.GetPid())]; ok {
			pu.InstanceId, pu.InstanceName = roots[root].id, roots[root].name
		}
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

// Reports a live instance with the name outside the slot, the instance self excluded
func (m *Manager) conflict(name, slotID, self string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conflictLocked(name, slotID, self)
}

func (m *Manager) conflictLocked(name, slotID, self string) bool {
	for _, in := range m.list {
		rec := in.snapshot()
		if rec.GetName() != name || Terminal(rec.GetState()) || rec.GetId() == self {
			continue
		}
		if slotID == "" || rec.GetSlotId() != slotID {
			return true
		}
	}
	return false
}

// Returns the slot reservation or an error if slots are not initialized
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
	for _, path := range m.rankLogs(id) {
		more, err := launch.ReadTail(path, 0)
		if err != nil {
			return nil
		}
		lines = append(lines, more...)
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
	for _, path := range m.rankLogs(id) {
		os.Remove(path)
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
		rec := m.list[i].snapshot()
		if Terminal(rec.GetState()) {
			m.list = append(m.list[:i], m.list[i+1:]...)
			m.forget(rec.GetId())
			finished--
			i--
		}
	}
}

// Returns the stored descriptor, rebuilding missing or outdated descriptors from local files
func (m *Manager) describe(ctx context.Context, stored *v1.StoredModel) (*v1.Descriptor, error) {
	if d := stored.GetDescriptor_(); d != nil && d.GetKind() != v1.ModelKind_MODEL_KIND_UNSPECIFIED && descriptor.Current(d) {
		return d, nil
	}
	return m.rebuild(ctx, stored)
}

// Backfills descriptors and runtime masks in old manifests
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
		if d == nil || d.GetKind() == v1.ModelKind_MODEL_KIND_UNSPECIFIED || diffusion.Undetected(d) || !descriptor.Current(d) {
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

// Restricts the profile to slot devices and caps placement memory pools at its budget
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

// Refuses a slot pin this node lacks or whose device holds no memory of its own, ids on other nodes left to the conductor
func pinnable(profile *v1.HostProfile, ids []string) error {
	for _, id := range ids {
		if strings.Contains(id, "/") {
			continue
		}
		d := host.Device(profile, id)
		if d == nil {
			return fmt.Errorf("%w: device %q was not probed, see nebu host", runtimes.ErrParam, id)
		}
		if !host.OwnMemory(profile, d) {
			return fmt.Errorf("%w: device %q (%s) has no memory of its own, so a slot cannot pin it", runtimes.ErrParam, id, d.GetName())
		}
	}
	return nil
}

// Device pins from the slot, or none
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

// Reports whether an instance state is final
func Terminal(s v1.InstanceState) bool {
	return s == v1.InstanceState_INSTANCE_STATE_STOPPED || s == v1.InstanceState_INSTANCE_STATE_FAILED
}
