// Package formations conducts models across mesh nodes: plans a formation over the mesh profile,
// pulls weights to the seats that need them, launches seats by phase, routes the head, watches
// every seat, and keeps every member's copy of the record current.
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
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/slots"
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
)

// Slots on this node: their refs for the node record, the names a slot's formation answers to,
// and where the slot follows its formation's state
type SlotView interface {
	Refs() []*v1.SlotRef
	Names(slotID string) []slots.RouteName
	OnFormation(f *v1.Formation)
	// Binds a run to its slot: the slot shapes the request, and an occupied slot refuses unless
	// a swap is under way
	Claim(ctx context.Context, slotID string, run *v1.RunRequest) (*v1.RunRequest, error)
	// The slot a run is bound to, for its placement, device pins, and memory budget
	Get(id string) (*v1.Slot, *v1.Instance, error)
}

// This node's gateway, for the canary a relay formation answers before it is ready
type LocalGateway interface {
	Client(origin string) *http.Client
	LocalBase() string
}

// Conducts formations and copies the ones other members conduct
type Manager struct {
	DB          *db.DB
	Mesh        *mesh.Manager
	Instances   *instances.Manager
	Routes      *gateway.Table
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
	// The gateway's request recorder, what route profiles are learned from and what proves a canary
	Traces *gateway.Recorder
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
	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Loads every formation record: the ones conducted here and the copies of others'
func (m *Manager) Open(ctx context.Context) error {
	if m.Log == nil {
		m.Log = slog.Default()
	}
	m.list = map[string]*v1.Formation{}
	m.busy = map[string]bool{}
	m.stopping = map[string]bool{}
	m.merged = map[string]time.Time{}
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

// Seats hosted on this node as the node record advertises them: every live seat, and a finished
// seat with its error and triage until the conductor's record has taken the failure
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
		if f.GetState() != v1.FormationState_FORMATION_STATE_READY && f.GetState() != v1.FormationState_FORMATION_STATE_STARTING {
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

// Applies a change to a conducted formation, sums its seats' measurements by node, stores it,
// and publishes it
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

// Sums every seat's measurements by node and key, seats in record order, the first line of a
// key kept as its example
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

// Whether a directory under the cache is still wanted: a model's tensor cache while any formation
// of the model is known here, a formation's own directory while the formation has not ended
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

// Takes the formations a member conducts, as its sync carried them: a copy with a higher sequence
// replaces ours, and one at the same sequence replaces a copy this node marked unreachable, since
// that mark is not the conductor's write. Ready ones are routed through the conductor's gateway
// and seats hosted here of ended ones are reaped. A copy the conductor no longer lists was
// deleted there, and goes here too
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
		m.routeCopy(copyOf)
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

// Drops a formation record with its routes and seats here
func (m *Manager) remove(f *v1.Formation, why string) {
	m.mu.Lock()
	delete(m.list, f.GetId())
	delete(m.merged, f.GetId())
	delete(m.stopping, f.GetId())
	m.mu.Unlock()
	m.Routes.RemoveFormation(f.GetId())
	if _, err := m.DB.DeleteFormation(context.Background(), f.GetId()); err != nil {
		m.Log.Warn("formation record delete failed", "id", f.GetId(), "err", err)
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_FORMATION, v1.EventAction_EVENT_ACTION_DELETED, f.GetId(), f)
	if seats := m.Instances.SeatsOf(f.GetId()); len(seats) > 0 {
		go m.reapSeats(seats, why)
	}
}

// Keeps this node's route for a formation another member conducts in step with its state. A
// route this gateway cannot serve, for a name already taken here, leaves the copy unreachable
// from this node with the reason on it.
func (m *Manager) routeCopy(f *v1.Formation) {
	switch f.GetState() {
	case v1.FormationState_FORMATION_STATE_READY, v1.FormationState_FORMATION_STATE_STARTING, v1.FormationState_FORMATION_STATE_DEGRADED:
		if f.GetEndpoint() == "" {
			return
		}
		if _, err := m.Routes.Forward(f, m.Runtimes.API(f.GetRuntimeId())); err != nil {
			m.Log.Warn("formation not routed through this gateway", "formation", f.GetName(), "conductor", f.GetConductorName(), "err", err)
			m.mu.Lock()
			copyOf, ok := m.list[f.GetId()]
			if ok {
				copyOf.State = v1.FormationState_FORMATION_STATE_UNREACHABLE
				copyOf.Error = "not routed through this node's gateway: " + err.Error()
				f = proto.Clone(copyOf).(*v1.Formation)
			}
			m.mu.Unlock()
			if ok {
				m.persist(f)
				m.Events.Publish(v1.EventKind_EVENT_KIND_FORMATION, v1.EventAction_EVENT_ACTION_UPDATED, f.GetId(), f)
			}
		}
	default:
		m.Routes.RemoveFormation(f.GetId())
	}
}

// Stops seats hosted here whose formation has ended or whose conductor is gone, so no process
// outlives the formation it served
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

// Follows a member's state: a conductor gone quiet takes its formations off every gateway, one
// gone for good takes its seats hosted here down, and a seat's node gone quiet degrades the
// formation it serves
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
					conducted = append(conducted, f)
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
				m.Routes.RemoveFormation(f.GetId())
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
			if f.GetState() == v1.FormationState_FORMATION_STATE_READY {
				go m.degrade(f.GetId(), fmt.Sprintf("%s, which hosts a seat, is %s", m.nodeName(nodeID), stateWord(state)))
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

// Takes up conducted formations after a restart: ones being stopped or not wanted have their
// seats stopped and end, and wanted ones adopt their seats, verifying each before serving again.
// Copies of other conductors' formations wait for their conductor's next sync.
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
	for _, id := range copies {
		m.Routes.RemoveFormation(id)
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
			m.Routes.RemoveFormation(f.GetId())
			m.stopSeats(m.base, f, nil)
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

// Adopts a formation after a restart: the head is verified first, from its instance on this node,
// then every other seat over the sync, each recorded as it verifies, and the route returns only
// when all of them did. A seat that does not verify ends the formation and launches it again.
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
		ended := m.update(f.GetId(), func(r *v1.Formation) {
			r.State = v1.FormationState_FORMATION_STATE_STOPPED
			r.Error = db.RestartNote + ": " + why
			r.StoppedAt = timestamppb.Now()
		})
		m.Routes.RemoveFormation(f.GetId())
		m.stopSeats(ctx, f, nil)
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
	if err := m.route(ctx, f.GetId()); err != nil {
		relaunch("route: " + err.Error())
		return
	}
	m.Log.Info("adopted formation, every seat verified", "formation", f.GetName())
	m.update(f.GetId(), func(r *v1.Formation) {
		if r.State == v1.FormationState_FORMATION_STATE_STARTING {
			r.State = v1.FormationState_FORMATION_STATE_READY
			r.Error = ""
		}
	})
	m.Mesh.Bump()
}

// Writes what a verified seat's node reports onto the record
func (m *Manager) recordVerified(id string, s *v1.Seat, in *v1.Instance) {
	m.update(id, func(r *v1.Formation) {
		for _, rs := range r.GetSeats() {
			if sameSeat(rs, s) {
				rs.State, rs.Error, rs.Triage = in.GetState(), "", nil
				rs.Transport = in.GetTransport()
				rs.Measurements = in.GetMeasurements()
				rs.Exposed = in.GetSeat().GetExposed() || in.GetTransport() == transportRDMA
			}
		}
	})
}

// Waits for a seat's instance to report ready from its node, this node's instances for a seat
// here and the seat's node otherwise, within the adoption window
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

// Waits until a record of the formation's conductor written after since says its head is ready,
// for a stage adopted on this node while a head holds it. The formation conducted here answers
// from the head's instance; another conductor's answers from the copy its sync brought.
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

// The seat answering the route: the role the runtime marks head
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

// Stops a formation: a launch under way is canceled and stops what it started, the route drains
// and requests in flight finish, then the head stops first, so no stage sees a connection drop
// mid request, then the stages together
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
	m.update(f.GetId(), func(r *v1.Formation) {
		r.DesiredRunning = false
		r.State = v1.FormationState_FORMATION_STATE_STOPPING
	})
	if launching && f.GetTaskId() != "" {
		if _, err := m.Tasks.Cancel(f.GetTaskId()); err == nil {
			if _, err := m.Tasks.Wait(ctx, f.GetTaskId()); err != nil {
				return nil, err
			}
		}
	}
	m.Routes.DrainFormation(f.GetId())
	current, err := m.Get(f.GetId())
	if err != nil {
		return nil, err
	}
	m.waitDrained(ctx, current)
	m.stopSeats(ctx, current, nil)
	m.Routes.RemoveFormation(f.GetId())
	out := m.update(f.GetId(), func(r *v1.Formation) {
		r.State = v1.FormationState_FORMATION_STATE_STOPPED
		r.StoppedAt = timestamppb.Now()
	})
	m.mu.Lock()
	delete(m.stopping, f.GetId())
	m.mu.Unlock()
	m.Mesh.Bump()
	return out, nil
}

// Waits for the requests in flight on every seat to finish, up to the drain timeout
func (m *Manager) waitDrained(ctx context.Context, f *v1.Formation) {
	limit := m.DrainTimeout
	if limit <= 0 {
		limit = defaultDrainTimeout
	}
	deadline := time.Now().Add(limit)
	for _, s := range f.GetSeats() {
		if s.GetInstanceId() == "" {
			continue
		}
		left := time.Until(deadline)
		if left <= 0 {
			return
		}
		if !m.Routes.WaitDrained(ctx, s.GetInstanceId(), left) {
			m.Log.Warn("requests still in flight when the seat stops", "formation", f.GetName(), "seat", s.GetRole(), "node", s.GetNodeName(), "in_flight", m.Routes.InFlight(s.GetInstanceId()))
		}
	}
}

// Stops every seat of a formation, the head first, then the rest together. Seats whose stop
// fails keep the error on their record.
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
