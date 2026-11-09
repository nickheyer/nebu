// Package formations conducts models across mesh nodes, planning them, pulling weights, launching seats by phase, and watching every seat
package formations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/calibrate"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/perf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	kindFormation = "formation"
	// Finished formations kept in the list
	historyMax = 50
	// A seat of a formation adopted after a restart has this long to verify over the sync
	adoptWindow = time.Minute
	// Seats of a formation being taken down get this long to stop
	stopTimeout = 2 * time.Minute
	// A draining formation reads its seats' requests in flight this often
	drainPoll = 100 * time.Millisecond
	// Requests in flight get this long before the head stops, when config names no drain timeout
	defaultDrainTimeout = 30 * time.Second
	// A seat is polled this often while the conductor verifies it
	verifyPoll  = time.Second
	callTimeout = 15 * time.Second
)

var (
	// Returned when a formation id or name is not known
	ErrUnknownFormation = errors.New("unknown formation")
	// Returned when a formation request is malformed or refused
	ErrFormation = errors.New("formation")
	// A formation whose seat died while serving launches again after this long
	relaunchDelay = 10 * time.Second
	// A formation ready this long before it degraded served a stretch, and its relaunch restarts the count
	relaunchStable = 5 * time.Minute
	// Relaunches in a row without a stretch served before a degraded formation stays failed
	maxRelaunches uint32 = 3
)

// Slots on this node, their refs for the node record, the names they answer to, and their formation hook
type SlotView interface {
	Refs() []*v1.SlotRef
	// Whether a slot answers to the name, as its own name or an alias
	Owns(name string) bool
	OnFormation(f *v1.Formation)
	// Binds a run to its slot, the slot shaping the request and an occupied slot refusing outside a swap
	Claim(ctx context.Context, slotID string, run *v1.RunRequest) (*v1.RunRequest, error)
	// The slot a run is bound to, for its placement, device pins, and memory budget
	Get(id string) (*v1.Slot, *v1.Instance, error)
}

// This node's gateway, for the canary a relay formation answers before it is ready
type LocalGateway interface {
	Client(origin string) *http.Client
	LocalBase() string
	// The trace the gateway recorded for the request a response answers
	Trace(resp *http.Response) (*v1.Trace, bool)
}

// Conducts formations and copies the ones other members conduct
type Manager struct {
	DB        *db.DB
	Mesh      *mesh.Manager
	Instances *instances.Manager
	// Publishes the seats of formations conducted here and names their routes
	Registrar pools.Registrar
	// Requests the gateway has in flight on a seat's instance, read while a formation drains
	InFlight    func(instanceID string) int
	Tasks       *tasks.Manager
	Inspector   *inspect.Inspector
	Runtimes    *runtimes.Registry
	Installs    *installs.Manager
	Store       *store.Store
	Puller      *pull.Puller
	Perf        *perf.Table
	Calibration *calibrate.Table
	Events      *events.Bus
	Log         *slog.Logger
	// Where stages keep the tensors their heads stream to them
	CacheDir string
	// Set by the daemon once slots exist
	SlotView SlotView
	// This node's gateway, for the canary a relay formation answers before it is ready
	Gateway LocalGateway
	// How long requests in flight get before the head stops
	DrainTimeout time.Duration
	// Plans a run request over the mesh, the formation planner unless a test installs another
	planner func(ctx context.Context, run *v1.RunRequest) (*planned, error)

	mu   sync.Mutex
	list map[string]*v1.Formation
	// Formations with an operation running
	busy map[string]bool
	// Formations a stop was asked for, so their seats' exits do not read as failures
	stopping map[string]bool
	// When each copy of another conductor's formation last arrived over the sync
	merged map[string]time.Time
	// Seat relaunches of replicas formations under way, cancelled by a stop, by formation and seat
	reseats map[string]map[string]*reseat
	// The next seat relaunch delay of each replicas formation and since when it has served whole
	backoffs map[string]*seatBackoff
	base     context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// Loads every formation record, those conducted here and the copies of other conductors'
func (m *Manager) Open(ctx context.Context) error {
	if m.Log == nil {
		m.Log = slog.Default()
	}
	m.list = map[string]*v1.Formation{}
	m.busy = map[string]bool{}
	m.stopping = map[string]bool{}
	m.merged = map[string]time.Time{}
	m.reseats = map[string]map[string]*reseat{}
	m.backoffs = map[string]*seatBackoff{}
	m.base, m.cancel = context.WithCancel(context.Background())
	records, err := m.DB.ListFormations(ctx)
	if err != nil {
		return err
	}
	for _, f := range records {
		f.Measurements = nodeSums(f.GetSeats())
		m.list[f.GetId()] = f
	}
	return nil
}

// This node's id
func (m *Manager) self() string { return m.Mesh.Self() }

// Starts watching seats and learning from traces
func (m *Manager) Start() {
	m.wg.Add(2)
	go func() {
		defer m.wg.Done()
		m.watch(m.base)
	}()
	go func() {
		defer m.wg.Done()
		m.learn(m.base)
	}()
}

// Stops watching
func (m *Manager) Close() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}

// Formations conducted on this node
func (m *Manager) Conducted() []*v1.Formation {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*v1.Formation
	for _, f := range m.list {
		if f.GetConductor() == m.self() {
			out = append(out, proto.Clone(f).(*v1.Formation))
		}
	}
	sortFormations(out)
	return out
}

// Seats this node hosts as its record advertises them, finished ones kept with triage until the conductor takes them
func (m *Manager) Seats() []*v1.SeatRef {
	refs := m.Instances.SeatRefs()
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*v1.SeatRef
	for _, ref := range refs {
		if !instances.Terminal(ref.GetState()) {
			out = append(out, ref)
			continue
		}
		f, ok := m.list[ref.GetFormationId()]
		if !ok || terminal(f.GetState()) {
			continue
		}
		taken := false
		for _, s := range f.GetSeats() {
			if s.GetInstanceId() == ref.GetInstanceId() && instances.Terminal(s.GetState()) {
				taken = true
			}
		}
		if !taken {
			out = append(out, ref)
		}
	}
	return out
}

// Slots on this node
func (m *Manager) Slots() []*v1.SlotRef {
	if m.SlotView == nil {
		return nil
	}
	return m.SlotView.Refs()
}

// Whether a formation with a seat on the node is serving
func (m *Manager) Serving(nodeID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.list {
		if !serving(f.GetState()) && f.GetState() != v1.FormationState_FORMATION_STATE_STARTING {
			continue
		}
		for _, s := range f.GetSeats() {
			if s.GetNodeId() == nodeID {
				return true
			}
		}
	}
	return false
}

// Every formation known, newest first
func (m *Manager) List(runningOnly bool) []*v1.Formation {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*v1.Formation
	for _, f := range m.list {
		if runningOnly && terminal(f.GetState()) {
			continue
		}
		out = append(out, proto.Clone(f).(*v1.Formation))
	}
	sortFormations(out)
	return out
}

// One formation by id or name, a live one before a finished one of the same name
func (m *Manager) Get(id string) (*v1.Formation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.findLocked(id)
	if err != nil {
		return nil, err
	}
	return proto.Clone(f).(*v1.Formation), nil
}

func (m *Manager) findLocked(id string) (*v1.Formation, error) {
	if f, ok := m.list[id]; ok {
		return f, nil
	}
	var match *v1.Formation
	for _, f := range m.list {
		if f.GetName() != id {
			continue
		}
		if !terminal(f.GetState()) {
			return f, nil
		}
		if match == nil || f.GetCreatedAt().AsTime().After(match.GetCreatedAt().AsTime()) {
			match = f
		}
	}
	if match == nil {
		return nil, fmt.Errorf("%w %q", ErrUnknownFormation, id)
	}
	return match, nil
}

func sortFormations(list []*v1.Formation) {
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].GetCreatedAt().AsTime().After(list[j].GetCreatedAt().AsTime())
	})
}

func terminal(s v1.FormationState) bool {
	switch s {
	case v1.FormationState_FORMATION_STATE_STOPPED, v1.FormationState_FORMATION_STATE_FAILED:
		return true
	}
	return false
}

// Whether a name is taken by a live formation
func (m *Manager) nameTaken(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.list {
		if f.GetName() == name && !terminal(f.GetState()) {
			return true
		}
	}
	return false
}

// Applies a change to a conducted formation, sums seat measurements by node, stores it and publishes it
func (m *Manager) update(id string, fn func(*v1.Formation)) *v1.Formation {
	m.mu.Lock()
	f, ok := m.list[id]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	fn(f)
	f.Measurements = nodeSums(f.GetSeats())
	f.Sequence++
	f.UpdatedAt = timestamppb.Now()
	out := proto.Clone(f).(*v1.Formation)
	m.pruneLocked()
	m.mu.Unlock()
	m.persist(out)
	m.Events.Publish(v1.EventKind_EVENT_KIND_FORMATION, v1.EventAction_EVENT_ACTION_UPDATED, id, out)
	if out.GetSlotId() != "" && m.SlotView != nil && out.GetConductor() == m.self() {
		m.SlotView.OnFormation(out)
	}
	return out
}

// Sums seat measurements by node and key in record order, keeping the first line of each key
func nodeSums(seats []*v1.Seat) []*v1.NodeMeasurements {
	var out []*v1.NodeMeasurements
	byNode := map[string]*v1.NodeMeasurements{}
	for _, s := range seats {
		if len(s.GetMeasurements()) == 0 {
			continue
		}
		node, ok := byNode[s.GetNodeId()]
		if !ok {
			node = &v1.NodeMeasurements{NodeId: s.GetNodeId(), NodeName: s.GetNodeName()}
			byNode[s.GetNodeId()] = node
			out = append(out, node)
		}
		for _, ms := range s.GetMeasurements() {
			found := false
			for _, have := range node.Measurements {
				if have.GetKey() == ms.GetKey() {
					have.Bytes += ms.GetBytes()
					found = true
					break
				}
			}
			if !found {
				node.Measurements = append(node.Measurements, &v1.Measurement{Key: ms.GetKey(), Bytes: ms.GetBytes(), Line: ms.GetLine()})
			}
		}
	}
	return out
}

// Whether a cache directory is wanted, a model cache while any formation uses it, a formation's until it ends
func (m *Manager) Live(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.list[name]; ok {
		return !terminal(f.GetState())
	}
	for _, f := range m.list {
		if f.GetCacheKey() != "" && f.GetCacheKey() == name {
			return true
		}
	}
	return false
}

// This node's record, for the slots that check devices on other members
func (m *Manager) Node(id string) (*v1.Node, error) { return m.Mesh.Node(id) }

// This node's id, for the slots
func (m *Manager) Self() string { return m.self() }

func (m *Manager) persist(f *v1.Formation) {
	if err := m.DB.PutFormation(context.Background(), f); err != nil {
		m.Log.Warn("formation record write failed", "id", f.GetId(), "err", err)
	}
}

// Drops finished formations past the history limit, oldest first
func (m *Manager) pruneLocked() {
	var finished []*v1.Formation
	for _, f := range m.list {
		if terminal(f.GetState()) {
			finished = append(finished, f)
		}
	}
	if len(finished) <= historyMax {
		return
	}
	sort.Slice(finished, func(i, j int) bool {
		return finished[i].GetCreatedAt().AsTime().Before(finished[j].GetCreatedAt().AsTime())
	})
	for _, f := range finished[:len(finished)-historyMax] {
		delete(m.list, f.GetId())
		delete(m.merged, f.GetId())
		go func(id string, rec *v1.Formation) {
			if _, err := m.DB.DeleteFormation(context.Background(), id); err != nil {
				m.Log.Warn("formation record delete failed", "id", id, "err", err)
			}
			m.Events.Publish(v1.EventKind_EVENT_KIND_FORMATION, v1.EventAction_EVENT_ACTION_DELETED, id, rec)
		}(f.GetId(), f)
	}
}

// Takes a member's conducted formations from its sync, newer copies replacing ours, reaping ended seats and dropping unlisted copies
func (m *Manager) Merge(conductor string, list []*v1.Formation) {
	if !m.Mesh.Joined() {
		return
	}
	listed := map[string]bool{}
	for _, f := range list {
		if f.GetConductor() != conductor || f.GetId() == "" {
			continue
		}
		listed[f.GetId()] = true
		m.mu.Lock()
		have, known := m.list[f.GetId()]
		if known && have.GetConductor() == m.self() {
			m.mu.Unlock()
			continue
		}
		if known {
			newer := f.GetSequence() > have.GetSequence()
			returned := f.GetSequence() == have.GetSequence() && have.GetState() == v1.FormationState_FORMATION_STATE_UNREACHABLE
			if !newer && !returned {
				m.mu.Unlock()
				continue
			}
		}
		copyOf := proto.Clone(f).(*v1.Formation)
		copyOf.Measurements = nodeSums(copyOf.GetSeats())
		m.list[f.GetId()] = copyOf
		m.merged[f.GetId()] = time.Now()
		m.mu.Unlock()
		m.persist(copyOf)
		action := v1.EventAction_EVENT_ACTION_UPDATED
		if !known {
			action = v1.EventAction_EVENT_ACTION_CREATED
		}
		m.Events.Publish(v1.EventKind_EVENT_KIND_FORMATION, action, f.GetId(), copyOf)
		if terminal(copyOf.GetState()) {
			go m.reapSeats(m.Instances.SeatsOf(copyOf.GetId()), fmt.Sprintf("%s reports %s %s", copyOf.GetConductorName(), copyOf.GetName(), formationWord(copyOf.GetState())))
		}
	}
	for _, f := range m.copiesOf(conductor) {
		if !listed[f.GetId()] {
			m.remove(f, fmt.Sprintf("%s no longer lists %s", f.GetConductorName(), f.GetName()))
		}
	}
}

// The copies held here of the formations a member conducts
func (m *Manager) copiesOf(conductor string) []*v1.Formation {
	if conductor == m.self() {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*v1.Formation
	for _, f := range m.list {
		if f.GetConductor() == conductor {
			out = append(out, proto.Clone(f).(*v1.Formation))
		}
	}
	return out
}

// Removes a formation record, a running one stopped first
func (m *Manager) Delete(ctx context.Context, id string) (*v1.Formation, error) {
	f, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if f.GetConductor() == m.self() {
		if !terminal(f.GetState()) {
			if f, err = m.Stop(ctx, f.GetId()); err != nil {
				return nil, err
			}
		}
	} else if rec, err := m.Mesh.Node(f.GetConductor()); err == nil && rec.GetState() == v1.NodeState_NODE_STATE_READY {
		return nil, fmt.Errorf("%w: %s is conducted by %s, delete it there", ErrFormation, f.GetName(), f.GetConductorName())
	}
	m.remove(f, "the formation was deleted")
	m.Mesh.Bump()
	return f, nil
}

// Stops conducted formations and drops every record
func (m *Manager) Clear(ctx context.Context) {
	for _, f := range m.List(false) {
		if f.GetConductor() == m.self() && !terminal(f.GetState()) {
			if _, err := m.Stop(ctx, f.GetId()); err != nil {
				m.Log.Warn("formation stop before leaving the mesh failed", "formation", f.GetName(), "err", err)
			}
		}
		m.remove(f, "this node left its mesh")
	}
}

// Drops a forgotten member's formation copies
func (m *Manager) Forget(nodeID string) {
	m.MemberState(nodeID, v1.NodeState_NODE_STATE_GONE)
	for _, f := range m.copiesOf(nodeID) {
		m.remove(f, fmt.Sprintf("their conductor %s was forgotten", f.GetConductorName()))
	}
}

// Drops a formation record with its endpoints, routes, seat relaunches and seats here
func (m *Manager) remove(f *v1.Formation, why string) {
	m.cancelReseats(f.GetId())
	m.mu.Lock()
	delete(m.list, f.GetId())
	delete(m.merged, f.GetId())
	delete(m.stopping, f.GetId())
	delete(m.backoffs, f.GetId())
	m.mu.Unlock()
	if f.GetConductor() == m.self() {
		m.withdraw(context.Background(), f.GetId())
	}
	if _, err := m.DB.DeleteFormation(context.Background(), f.GetId()); err != nil {
		m.Log.Warn("formation record delete failed", "id", f.GetId(), "err", err)
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_FORMATION, v1.EventAction_EVENT_ACTION_DELETED, f.GetId(), f)
	if seats := m.Instances.SeatsOf(f.GetId()); len(seats) > 0 {
		go m.reapSeats(seats, why)
	}
}

// Stops seats hosted here whose formation ended or whose conductor is gone, so no process outlives its formation
func (m *Manager) reapSeats(seats []*v1.Instance, why string) {
	for _, in := range seats {
		m.Log.Info("reaping a seat", "seat", in.GetName(), "why", why)
		ctx, cancel := context.WithTimeout(m.base, stopTimeout)
		if _, err := m.Instances.Stop(ctx, in.GetId()); err != nil && !errors.Is(err, instances.ErrUnknownInstance) {
			m.Log.Warn("seat reap failed", "seat", in.GetName(), "err", err)
		}
		cancel()
	}
}

// Follows a member's state, unreachable conductors marking copies, gone ones reaping seats here, lost seat nodes degrading formations
func (m *Manager) MemberState(nodeID string, state v1.NodeState) {
	m.mu.Lock()
	var copies, conducted []*v1.Formation
	for _, f := range m.list {
		switch {
		case f.GetConductor() == nodeID && f.GetConductor() != m.self():
			copies = append(copies, f)
		case f.GetConductor() == m.self():
			for _, s := range f.GetSeats() {
				if s.GetNodeId() == nodeID {
					conducted = append(conducted, proto.Clone(f).(*v1.Formation))
					break
				}
			}
		}
	}
	m.mu.Unlock()
	for _, f := range copies {
		switch state {
		case v1.NodeState_NODE_STATE_UNREACHABLE, v1.NodeState_NODE_STATE_GONE:
			if !terminal(f.GetState()) && f.GetState() != v1.FormationState_FORMATION_STATE_UNREACHABLE {
				m.mu.Lock()
				f.State = v1.FormationState_FORMATION_STATE_UNREACHABLE
				out := proto.Clone(f).(*v1.Formation)
				m.mu.Unlock()
				m.persist(out)
				m.Events.Publish(v1.EventKind_EVENT_KIND_FORMATION, v1.EventAction_EVENT_ACTION_UPDATED, f.GetId(), out)
			}
		}
	}
	if state == v1.NodeState_NODE_STATE_GONE {
		if seats := m.Instances.SeatsConductedBy(nodeID); len(seats) > 0 {
			go m.reapSeats(seats, fmt.Sprintf("their conductor %s is gone", m.nodeName(nodeID)))
		}
	}
	if state == v1.NodeState_NODE_STATE_UNREACHABLE || state == v1.NodeState_NODE_STATE_GONE {
		for _, f := range conducted {
			why := fmt.Sprintf("%s, which hosts a seat, is %s", m.nodeName(nodeID), stateWord(state))
			for _, s := range f.GetSeats() {
				if s.GetNodeId() == nodeID {
					m.seatLeft(f, s, why)
				}
			}
		}
	}
}

func stateWord(s v1.NodeState) string {
	switch s {
	case v1.NodeState_NODE_STATE_UNREACHABLE:
		return "unreachable"
	case v1.NodeState_NODE_STATE_GONE:
		return "gone"
	case v1.NodeState_NODE_STATE_READY:
		return "ready"
	}
	return "unknown"
}

func formationWord(s v1.FormationState) string {
	switch s {
	case v1.FormationState_FORMATION_STATE_STOPPED:
		return "stopped"
	case v1.FormationState_FORMATION_STATE_FAILED:
		return "failed"
	case v1.FormationState_FORMATION_STATE_READY:
		return "ready"
	case v1.FormationState_FORMATION_STATE_STARTING:
		return "starting"
	case v1.FormationState_FORMATION_STATE_DEGRADED:
		return "degraded"
	case v1.FormationState_FORMATION_STATE_STOPPING:
		return "stopping"
	case v1.FormationState_FORMATION_STATE_UNREACHABLE:
		return "unreachable"
	}
	return "unknown"
}

// Takes up conducted formations after a restart, stopping unwanted ones and adopting wanted ones seat by seat
func (m *Manager) Recover(ctx context.Context) error {
	var adopt, stop []*v1.Formation
	var copies []string
	m.mu.Lock()
	for _, f := range m.list {
		if f.GetConductor() != m.self() {
			if !terminal(f.GetState()) {
				f.State = v1.FormationState_FORMATION_STATE_UNREACHABLE
			}
			copies = append(copies, f.GetId())
			continue
		}
		switch f.GetState() {
		case v1.FormationState_FORMATION_STATE_STOPPING:
			stop = append(stop, proto.Clone(f).(*v1.Formation))
		case v1.FormationState_FORMATION_STATE_STARTING, v1.FormationState_FORMATION_STATE_READY, v1.FormationState_FORMATION_STATE_DEGRADED:
			if f.GetDesiredRunning() {
				adopt = append(adopt, proto.Clone(f).(*v1.Formation))
			} else {
				stop = append(stop, proto.Clone(f).(*v1.Formation))
			}
		}
	}
	m.mu.Unlock()
	if len(copies) > 0 {
		m.Log.Info("copies of other conductors' formations wait for their next sync", "formations", len(copies))
	}
	for _, f := range adopt {
		m.update(f.GetId(), func(r *v1.Formation) {
			r.State = v1.FormationState_FORMATION_STATE_STARTING
			r.Error = "verifying every seat after a daemon restart"
		})
	}
	if len(stop) == 0 && len(adopt) == 0 {
		return nil
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for _, f := range stop {
			if ctx.Err() != nil {
				return
			}
			m.Log.Info("finishing the stop a previous daemon began", "formation", f.GetName())
			m.drain(m.base, f)
			m.stopSeats(m.base, f, nil)
			m.withdraw(m.base, f.GetId())
			m.update(f.GetId(), func(r *v1.Formation) {
				r.State = v1.FormationState_FORMATION_STATE_STOPPED
				if r.Error == "" {
					r.Error = db.RestartNote
				}
				r.StoppedAt = timestamppb.Now()
			})
		}
		for _, f := range adopt {
			if ctx.Err() != nil {
				return
			}
			m.adopt(m.base, f)
		}
		m.Mesh.Bump()
	}()
	return nil
}

// Adopts a formation after a restart, verifying the head then every seat, relaunching it when any seat fails to verify
func (m *Manager) adopt(ctx context.Context, f *v1.Formation) {
	m.mu.Lock()
	m.busy[f.GetId()] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.busy, f.GetId())
		m.mu.Unlock()
	}()
	relaunch := func(why string) {
		if m.isStopping(f.GetId()) {
			return
		}
		m.Log.Info("relaunching formation after the restart", "formation", f.GetName(), "why", why)
		m.drain(ctx, f)
		m.stopSeats(ctx, f, nil)
		m.withdraw(ctx, f.GetId())
		ended := m.update(f.GetId(), func(r *v1.Formation) {
			r.State = v1.FormationState_FORMATION_STATE_STOPPED
			r.Error = db.RestartNote + ": " + why
			r.StoppedAt = timestamppb.Now()
			r.Conditions = instances.SetCondition(r.Conditions, CondLaunched, false, ReasonStopped, r.Error, r.GetGeneration())
			r.Conditions = instances.SetCondition(r.Conditions, CondReady, false, ReasonStopped, r.Error, r.GetGeneration())
		})
		if ctx.Err() != nil || !ended.GetDesiredRunning() {
			return
		}
		if _, _, err := m.relaunch(ctx, f); err != nil {
			m.Log.Warn("formation relaunch failed", "formation", f.GetName(), "err", err)
			m.update(f.GetId(), func(r *v1.Formation) { r.Error = "relaunch failed: " + err.Error() })
		}
	}
	head := m.headSeat(f)
	if head == nil || head.GetInstanceId() == "" {
		relaunch("the record names no head instance")
		return
	}
	in, err := m.verifySeat(ctx, head)
	if err != nil {
		relaunch(fmt.Sprintf("the head on %s did not verify: %v", head.GetNodeName(), err))
		return
	}
	m.recordVerified(f.GetId(), head, in)
	m.Mesh.Bump()
	rest := make([]*v1.Seat, 0, len(f.GetSeats()))
	for _, s := range f.GetSeats() {
		if !sameSeat(s, head) {
			rest = append(rest, s)
		}
	}
	errs := make([]error, len(rest))
	var wg sync.WaitGroup
	for i, s := range rest {
		wg.Add(1)
		go func(i int, s *v1.Seat) {
			defer wg.Done()
			in, err := m.verifySeat(ctx, s)
			if err != nil {
				errs[i] = fmt.Errorf("%s on %s did not verify: %w", s.GetRole(), s.GetNodeName(), err)
				return
			}
			m.recordVerified(f.GetId(), s, in)
		}(i, s)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			relaunch(err.Error())
			return
		}
	}
	if err := m.publishSeats(ctx, f.GetId(), nil); err != nil {
		relaunch("publish: " + err.Error())
		return
	}
	if err := m.route(ctx, f.GetId()); err != nil {
		relaunch("route: " + err.Error())
		return
	}
	m.Log.Info("adopted formation, every seat verified", "formation", f.GetName())
	m.update(f.GetId(), func(r *v1.Formation) {
		if r.State == v1.FormationState_FORMATION_STATE_STARTING {
			markReady(r)
		}
	})
	m.servedWhole(f.GetId())
	m.Mesh.Bump()
}

// Writes what a verified seat's node reports onto the record
func (m *Manager) recordVerified(id string, s *v1.Seat, in *v1.Instance) {
	m.update(id, func(r *v1.Formation) {
		for _, rs := range r.GetSeats() {
			if sameSeat(rs, s) {
				rs.State, rs.Error, rs.Triage = in.GetState(), "", nil
				rs.Conditions = seatConditions(in.GetConditions())
				rs.Transport = in.GetTransport()
				rs.Measurements = in.GetMeasurements()
				rs.Exposed = in.GetSeat().GetExposed() || in.GetTransport() == transportRDMA
			}
		}
	})
}

// Waits within the adoption window for a seat's instance to report ready from this node or the seat's node
func (m *Manager) verifySeat(ctx context.Context, s *v1.Seat) (*v1.Instance, error) {
	if s.GetInstanceId() == "" {
		return nil, errors.New("the record names no instance")
	}
	deadline := time.Now().Add(adoptWindow)
	var last error
	for {
		var in *v1.Instance
		var err error
		if s.GetNodeId() == m.self() {
			in, err = m.Instances.Get(s.GetInstanceId())
		} else {
			in, err = m.remoteSeat(ctx, s.GetNodeId(), s.GetInstanceId())
		}
		switch {
		case err == nil && in.GetState() == v1.InstanceState_INSTANCE_STATE_READY:
			return in, nil
		case err == nil && instances.Terminal(in.GetState()):
			return in, errors.New(seatDetail(in))
		case err != nil:
			last = err
		default:
			last = fmt.Errorf("its instance is %s", instanceWord(in.GetState()))
		}
		if time.Now().After(deadline) {
			return in, fmt.Errorf("not ready within %s: %w", adoptWindow, last)
		}
		select {
		case <-ctx.Done():
			return in, ctx.Err()
		case <-time.After(verifyPoll):
		}
	}
}

// Waits for a conductor record newer than since that says the head is ready, for a stage a head holds
func (m *Manager) HeadReady(ctx context.Context, formationID string, since time.Time) error {
	for {
		m.mu.Lock()
		f, ok := m.list[formationID]
		var mergedAt time.Time
		var copyOf *v1.Formation
		if ok {
			mergedAt = m.merged[formationID]
			copyOf = proto.Clone(f).(*v1.Formation)
		}
		m.mu.Unlock()
		switch {
		case !ok:
		case terminal(copyOf.GetState()):
			return fmt.Errorf("%s is %s", copyOf.GetName(), formationWord(copyOf.GetState()))
		case copyOf.GetConductor() == m.self():
			head := m.headSeat(copyOf)
			if head != nil && head.GetInstanceId() != "" {
				in, err := m.Instances.Get(head.GetInstanceId())
				switch {
				case err == nil && in.GetState() == v1.InstanceState_INSTANCE_STATE_READY:
					return nil
				case err == nil && instances.Terminal(in.GetState()):
					return fmt.Errorf("the head is %s: %s", instanceWord(in.GetState()), seatDetail(in))
				}
			}
		case mergedAt.After(since):
			head := m.headSeat(copyOf)
			switch {
			case head == nil:
				return fmt.Errorf("%s names no head seat", copyOf.GetName())
			case head.GetState() == v1.InstanceState_INSTANCE_STATE_READY:
				return nil
			case instances.Terminal(head.GetState()):
				return fmt.Errorf("the head on %s is %s: %s", head.GetNodeName(), instanceWord(head.GetState()), head.GetError())
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("no record of the head ready arrived from the conductor: %w", ctx.Err())
		case <-time.After(verifyPoll):
		}
	}
}

// The seat answering the route, the role the runtime marks head
func (m *Manager) headSeat(f *v1.Formation) *v1.Seat {
	rt, err := m.Runtimes.Get(f.GetRuntimeId())
	if err == nil {
		for _, role := range rt.Roles(f.GetShape()) {
			if !role.Head {
				continue
			}
			for _, s := range f.GetSeats() {
				if s.GetRole() == role.Name && (s.GetRank() == 0 || f.GetShape() == v1.Shape_SHAPE_RELAY || f.GetShape() == v1.Shape_SHAPE_REPLICAS) && s.GetNodeId() == m.self() {
					return s
				}
			}
			for _, s := range f.GetSeats() {
				if s.GetRole() == role.Name {
					return s
				}
			}
		}
	}
	for _, s := range f.GetSeats() {
		if s.GetNodeId() == f.GetConductor() {
			return s
		}
	}
	if len(f.GetSeats()) > 0 {
		return f.GetSeats()[len(f.GetSeats())-1]
	}
	return nil
}

// Cancels any launch, drains requests, then stops the head before the stages so no stage sees a drop mid request
func (m *Manager) Stop(ctx context.Context, id string) (*v1.Formation, error) {
	f, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if f.GetConductor() != m.self() {
		return nil, fmt.Errorf("%w: %s is conducted by %s, stop it there", ErrFormation, f.GetName(), f.GetConductorName())
	}
	if terminal(f.GetState()) {
		return f, nil
	}
	m.mu.Lock()
	m.stopping[f.GetId()] = true
	launching := m.busy[f.GetId()]
	m.mu.Unlock()
	m.cancelReseats(f.GetId())
	m.update(f.GetId(), func(r *v1.Formation) {
		r.DesiredRunning = false
		r.State = v1.FormationState_FORMATION_STATE_STOPPING
		r.Conditions = instances.SetCondition(r.Conditions, CondReady, false, ReasonStopping, "", r.GetGeneration())
	})
	if launching && f.GetTaskId() != "" {
		if _, err := m.Tasks.Cancel(f.GetTaskId()); err == nil {
			if _, err := m.Tasks.Wait(ctx, f.GetTaskId()); err != nil {
				return nil, err
			}
		}
	}
	current, err := m.Get(f.GetId())
	if err != nil {
		return nil, err
	}
	m.drain(ctx, current)
	m.stopSeats(ctx, current, nil)
	m.withdraw(ctx, f.GetId())
	out := m.update(f.GetId(), func(r *v1.Formation) {
		r.State = v1.FormationState_FORMATION_STATE_STOPPED
		r.StoppedAt = timestamppb.Now()
		r.Conditions = instances.SetCondition(r.Conditions, CondLaunched, false, ReasonStopped, "", r.GetGeneration())
		r.Conditions = instances.SetCondition(r.Conditions, CondReady, false, ReasonStopped, "", r.GetGeneration())
		r.Conditions = instances.SetCondition(r.Conditions, CondDegraded, false, ReasonStopped, "", r.GetGeneration())
	})
	m.mu.Lock()
	delete(m.stopping, f.GetId())
	m.mu.Unlock()
	m.Mesh.Bump()
	return out, nil
}

// Takes the formation's endpoints out of traffic and waits for every seat's requests in flight, up to the drain timeout
func (m *Manager) drain(ctx context.Context, f *v1.Formation) {
	m.Registrar.Drain(ctx, ownerOf(f.GetId()))
	limit := m.DrainTimeout
	if limit <= 0 {
		limit = defaultDrainTimeout
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(drainPoll)
	defer tick.Stop()
	for {
		left := 0
		for _, s := range f.GetSeats() {
			if s.GetInstanceId() != "" {
				left += m.InFlight(s.GetInstanceId())
			}
		}
		if left == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			m.Log.Warn("requests still in flight when the formation's seats stop", "formation", f.GetName(), "in_flight", left)
			return
		case <-tick.C:
		}
	}
}

// Stops every seat with the head first and the rest together, a failed stop leaving its error on the seat
func (m *Manager) stopSeats(ctx context.Context, f *v1.Formation, h *tasks.Handle) {
	head := m.headSeat(f)
	logf := func(format string, args ...any) {
		if h != nil {
			h.Logf(format, args...)
		}
	}
	stop := func(s *v1.Seat) {
		if s.GetInstanceId() == "" {
			return
		}
		if err := m.stopSeat(ctx, s); err != nil {
			logf("stop %s on %s: %v", s.GetRole(), s.GetNodeName(), err)
			m.update(f.GetId(), func(r *v1.Formation) {
				for _, rs := range r.GetSeats() {
					if rs.GetInstanceId() == s.GetInstanceId() && rs.GetError() == "" {
						rs.Error = "stop: " + err.Error()
					}
				}
			})
			return
		}
		logf("stopped %s on %s", s.GetRole(), s.GetNodeName())
	}
	if head != nil {
		stop(head)
	}
	var wg sync.WaitGroup
	for _, s := range f.GetSeats() {
		if s == head || head != nil && s.GetInstanceId() == head.GetInstanceId() {
			continue
		}
		wg.Add(1)
		go func(s *v1.Seat) {
			defer wg.Done()
			stop(s)
		}(s)
	}
	wg.Wait()
}

// Whether the formation's stop was asked for, so a seat's exit is expected
func (m *Manager) isStopping(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopping[id]
}
