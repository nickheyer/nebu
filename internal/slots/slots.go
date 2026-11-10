// Package slots reserves devices under stable names, keeps their occupants reconciled, and swaps them
package slots

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/text"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const kindSwap = "swap"

var (
	// Returned when a slot id or name is not known
	ErrUnknownSlot = errors.New("unknown slot")
	// Returned when a slot is busy or a request malformed
	ErrSlot = errors.New("invalid slot request")
)

var (
	// How often every desired slot is checked for a missing occupant
	reconcileEvery = 5 * time.Second
	// How often a canary's health is read during each step
	canaryPoll = 250 * time.Millisecond
)

// Condition types a slot reports
const (
	CondAccepted = "Accepted"
	CondOccupied = "Occupied"
	CondReady    = "Ready"
	CondSwapping = "Swapping"
	CondCanary   = "Canary"
)

// Swap modes, the reasons a Swapping condition carries
const (
	ModeBlueGreen  = "BlueGreen"
	ModeDrainFirst = "DrainFirst"
	ModeCanary     = "Canary"
)

// The routes other owners named, so a slot never takes a name one of them holds
type Routes interface {
	Route(name string) (*v1.Route, bool)
}

// Manages slots and swaps, run and swap setting request and desire, evict and user stops clearing both
type Manager struct {
	DB        *db.DB
	Instances *instances.Manager
	// Declares each slot's pool and names its routes for the gateway
	Registrar pools.Registrar
	Routes    Routes
	Tasks     *tasks.Manager
	Host      *host.Prober
	Events    *events.Bus
	// What the gateway counted per split target, read across each canary step
	Outcomes Outcomes
	// Times canary steps, the system clock when nil
	Clock Clock
	Log   *slog.Logger
	// The conductor, set by the daemon once formations exist
	Formations Formations

	mu    sync.Mutex
	slots map[string]*v1.Slot
	// Slots with active operations
	busy map[string]bool
	// The mode of each swap under way
	modes map[string]string
	// The phase of each canary under way, reported as the slot's Canary condition
	phases map[string]phase
}

// The owner a slot declares its pool and names its routes under
func ownerOf(id string) pools.OwnerRef { return pools.OwnerRef{Kind: pools.OwnerSlot, ID: id} }

// Claims a slot unless another operation or swap holds it
func (m *Manager) claim(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.slots[id]
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownSlot, id)
	}
	if m.busy[id] {
		return fmt.Errorf("%w: slot %s is busy", ErrSlot, s.GetName())
	}
	if s.GetState() == v1.SlotState_SLOT_STATE_SWAPPING {
		return fmt.Errorf("%w: slot %s is already swapping", ErrSlot, s.GetName())
	}
	if m.busy == nil {
		m.busy = map[string]bool{}
	}
	m.busy[id] = true
	return nil
}

func (m *Manager) release(id string) {
	m.mu.Lock()
	delete(m.busy, id)
	m.mu.Unlock()
}

func (m *Manager) isBusy(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy[id]
}

// Loads every slot from the store, giving older slots the desire their request implied and clearing inherits of gone slots
func (m *Manager) Load(ctx context.Context) error {
	list, err := m.DB.ListSlots(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.slots = map[string]*v1.Slot{}
	for _, s := range list {
		if s.GetMeta() == nil {
			s.Meta = &v1.ObjectMeta{Generation: 1}
			s.DesiredRunning = s.GetRequest() != nil && s.GetState() != v1.SlotState_SLOT_STATE_EMPTY && s.GetState() != v1.SlotState_SLOT_STATE_FAILED
		}
		m.slots[s.GetId()] = s
	}
	var heirs []*v1.Slot
	for _, s := range m.slots {
		if m.unheritLocked(s) {
			heirs = append(heirs, m.bumpLocked(s))
		}
	}
	m.mu.Unlock()
	m.persist(heirs)
	m.renumber()
	return nil
}

// Takes up occupants the instances recovered, relaunches desired slots that lost theirs, and starts reconciling
func (m *Manager) Recover(ctx context.Context) error {
	var relaunch []*v1.Slot
	for _, s := range m.List() {
		if instances.ConditionTrue(s.GetConditions(), CondCanary) {
			m.abandonCanary(ctx, s)
		}
		live := m.liveInstance(s)
		switch {
		case s.GetFormationId() != "" && s.GetRequest() != nil && s.GetState() != v1.SlotState_SLOT_STATE_FAILED:
			// The conductor adopts or relaunches the formation, and the slot follows its record
			m.update(s.GetId(), func(sl *v1.Slot) { sl.InstanceId, sl.State, sl.Error = "", v1.SlotState_SLOT_STATE_STARTING, "" })
		case live != nil:
			s = m.update(s.GetId(), func(sl *v1.Slot) {
				sl.InstanceId, sl.DesiredRunning = live.GetId(), true
				sl.State = stateOf(live)
				if live.GetRequest() != nil {
					sl.Request = live.GetRequest()
				}
			})
			if err := m.claimRoutes(ctx, s, occupant{in: live}); err != nil {
				m.Log.Warn("slot routes not named", "slot", s.GetName(), "err", err)
			}
		case s.GetRequest() != nil && s.GetState() == v1.SlotState_SLOT_STATE_FAILED:
			m.update(s.GetId(), func(sl *v1.Slot) { sl.InstanceId = "" })
		case s.GetRequest() != nil && s.GetDesiredRunning():
			m.update(s.GetId(), func(sl *v1.Slot) {
				sl.InstanceId, sl.State, sl.Error = "", v1.SlotState_SLOT_STATE_STARTING, ""
			})
			relaunch = append(relaunch, s)
		default:
			m.update(s.GetId(), func(sl *v1.Slot) {
				sl.InstanceId, sl.State, sl.Error, sl.TaskId, sl.Request, sl.DesiredRunning = "", v1.SlotState_SLOT_STATE_EMPTY, "", "", nil, false
			})
			m.dropRoutes(ctx, s)
		}
	}
	every := reconcileEvery
	go func() {
		m.relaunchAll(ctx, relaunch)
		m.reconcile(ctx, every)
	}()
	return nil
}

// Relaunches slots serially so each plan accounts for earlier launches
func (m *Manager) relaunchAll(ctx context.Context, list []*v1.Slot) {
	for _, s := range list {
		if ctx.Err() != nil {
			return
		}
		m.Log.Info("relaunching slot", "slot", s.GetName(), "model", modelOf(s.GetRequest()))
		_, _, task, err := m.Relaunch(ctx, s.GetId())
		if err != nil {
			m.Log.Warn("slot relaunch failed", "slot", s.GetName(), "err", err)
			continue
		}
		m.Tasks.Watch(ctx, task.GetId(), func(*v1.WatchTaskResponse) error { return nil })
	}
}

// Keeps every desired slot occupied until ctx ends, recreating an occupant whose record is gone
func (m *Manager) reconcile(ctx context.Context, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		for _, s := range m.List() {
			m.reconcileSlot(ctx, s)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Recreates the occupant of a desired slot when its instance record is gone
func (m *Manager) reconcileSlot(ctx context.Context, s *v1.Slot) {
	if !s.GetDesiredRunning() || s.GetRequest() == nil || s.GetState() == v1.SlotState_SLOT_STATE_SWAPPING || s.GetState() == v1.SlotState_SLOT_STATE_FAILED || m.isBusy(s.GetId()) {
		return
	}
	if s.GetFormationId() != "" || m.meshRun(s, s.GetRequest()) {
		return
	}
	if s.GetInstanceId() != "" {
		if _, err := m.Instances.Get(s.GetInstanceId()); !errors.Is(err, instances.ErrUnknownInstance) {
			return
		}
	}
	m.Log.Info("slot occupant record is gone, launching it again", "slot", s.GetName(), "instance", s.GetInstanceId())
	if _, _, _, err := m.Relaunch(ctx, s.GetId()); err != nil {
		m.Log.Warn("slot occupant not recreated", "slot", s.GetName(), "err", err)
	}
}

// The slot state an occupant's state puts the slot in
func stateOf(in *v1.Instance) v1.SlotState {
	switch in.GetState() {
	case v1.InstanceState_INSTANCE_STATE_READY:
		return v1.SlotState_SLOT_STATE_READY
	case v1.InstanceState_INSTANCE_STATE_IDLE:
		return v1.SlotState_SLOT_STATE_IDLE
	case v1.InstanceState_INSTANCE_STATE_DRAINING, v1.InstanceState_INSTANCE_STATE_STOPPING:
		return v1.SlotState_SLOT_STATE_DRAINING
	case v1.InstanceState_INSTANCE_STATE_FAILED:
		return v1.SlotState_SLOT_STATE_FAILED
	}
	return v1.SlotState_SLOT_STATE_STARTING
}

func (m *Manager) liveInstance(s *v1.Slot) *v1.Instance {
	list := m.Instances.InSlot(s.GetId())
	for _, in := range list {
		if in.GetId() == s.GetInstanceId() {
			return in
		}
	}
	if len(list) > 0 {
		return list[0]
	}
	return nil
}

// Lists slots by position
func (m *Manager) List() []*v1.Slot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked()
}

func (m *Manager) listLocked() []*v1.Slot {
	out := make([]*v1.Slot, 0, len(m.slots))
	for _, s := range m.slots {
		out = append(out, proto.Clone(s).(*v1.Slot))
	}
	sortSlots(out)
	return out
}

func sortSlots(list []*v1.Slot) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].GetPosition() != list[j].GetPosition() {
			return list[i].GetPosition() < list[j].GetPosition()
		}
		return list[i].GetName() < list[j].GetName()
	})
}

// Assigns positions from 1 in list order and returns changed slots, the caller holding the lock
func (m *Manager) reorderLocked(ordered []*v1.Slot) []*v1.Slot {
	var moved []*v1.Slot
	for i, s := range ordered {
		if want := uint32(i + 1); s.GetPosition() != want {
			s.Position = want
			s.UpdatedAt = timestamppb.Now()
			moved = append(moved, proto.Clone(s).(*v1.Slot))
		}
	}
	return moved
}

// Stores and publishes changed slot positions
func (m *Manager) persist(moved []*v1.Slot) {
	for _, s := range moved {
		if err := m.DB.PutSlot(context.Background(), s); err != nil {
			m.Log.Warn("slot record write failed", "id", s.GetId(), "err", err)
		}
		m.Events.Publish(v1.EventKind_EVENT_KIND_SLOT, v1.EventAction_EVENT_ACTION_UPDATED, s.GetId(), s)
	}
}

// Renumbers slots in their current sort order
func (m *Manager) renumber() {
	m.mu.Lock()
	ordered := make([]*v1.Slot, 0, len(m.slots))
	for _, s := range m.slots {
		ordered = append(ordered, s)
	}
	sortSlots(ordered)
	moved := m.reorderLocked(ordered)
	m.mu.Unlock()
	m.persist(moved)
}

// Moves a slot and shifts neighboring positions
func (m *Manager) place(id string, position uint32) {
	m.mu.Lock()
	target, ok := m.slots[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	others := make([]*v1.Slot, 0, len(m.slots))
	for _, s := range m.slots {
		if s.GetId() != id {
			others = append(others, s)
		}
	}
	sortSlots(others)
	at := min(max(int(position)-1, 0), len(others))
	ordered := append(append(append([]*v1.Slot{}, others[:at]...), target), others[at:]...)
	moved := m.reorderLocked(ordered)
	m.mu.Unlock()
	m.persist(moved)
}

// Returns one slot by id or name with its occupant
func (m *Manager) Get(id string) (*v1.Slot, *v1.Instance, error) {
	s, err := m.find(id)
	if err != nil {
		return nil, nil, err
	}
	var in *v1.Instance
	if s.GetInstanceId() != "" {
		in, _ = m.Instances.Get(s.GetInstanceId())
	}
	return s, in, nil
}

func (m *Manager) find(id string) (*v1.Slot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.slots[id]; ok {
		return proto.Clone(s).(*v1.Slot), nil
	}
	for _, s := range m.slots {
		if s.GetName() == id {
			return proto.Clone(s).(*v1.Slot), nil
		}
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownSlot, id)
}

func (m *Manager) mustFind(id string) *v1.Slot {
	s, _ := m.find(id)
	return s
}

// Whether a slot answers to the name, as its own name or an alias
func (m *Manager) Owns(name string) bool {
	if _, err := m.find(name); err == nil {
		return true
	}
	return m.aliasOwner(name) != nil
}

// Applies fn under the lock, derives the conditions, writes, and publishes the slot
func (m *Manager) update(id string, fn func(*v1.Slot)) *v1.Slot {
	snapshot := m.apply(id, fn)
	if snapshot == nil {
		return nil
	}
	if err := m.DB.PutSlot(context.Background(), snapshot); err != nil {
		m.Log.Warn("slot record write failed", "id", id, "err", err)
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_SLOT, v1.EventAction_EVENT_ACTION_UPDATED, id, snapshot)
	return snapshot
}

// Changes the held slot and derives its conditions, releasing the lock even when fn panics, nil for an unknown slot
func (m *Manager) apply(id string, fn func(*v1.Slot)) *v1.Slot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.slots[id]
	if !ok {
		return nil
	}
	fn(s)
	m.statusLocked(s)
	s.UpdatedAt = timestamppb.Now()
	return proto.Clone(s).(*v1.Slot)
}

// Bumps the held slot's generation and derives its status as update does, returning a copy, the caller holding the lock
func (m *Manager) bumpLocked(s *v1.Slot) *v1.Slot {
	bump(s)
	m.statusLocked(s)
	s.UpdatedAt = timestamppb.Now()
	return proto.Clone(s).(*v1.Slot)
}

// Bumps the spec generation the slot's status is measured against
func bump(s *v1.Slot) {
	if s.Meta == nil {
		s.Meta = &v1.ObjectMeta{}
	}
	s.Meta.Generation++
	s.Meta.UpdatedAt = timestamppb.Now()
}

// Derives the slot's conditions from its state, occupant and swap mode, the caller holding the lock
func (m *Manager) statusLocked(s *v1.Slot) {
	if s.Meta == nil {
		s.Meta = &v1.ObjectMeta{Generation: 1}
	}
	gen := s.GetMeta().GetGeneration()
	set := func(typ string, ok bool, reason, message string) {
		s.Conditions = instances.SetCondition(s.Conditions, typ, ok, reason, message, gen)
	}
	set(CondAccepted, true, "Valid", "")
	switch {
	case s.GetInstanceId() != "":
		set(CondOccupied, true, "Instance", s.GetInstanceId())
	case s.GetFormationId() != "":
		set(CondOccupied, true, "Formation", s.GetFormationId())
	default:
		set(CondOccupied, false, "Empty", "")
	}
	mode, held := m.modes[s.GetId()]
	if !held {
		// A slot with no mode held keeps the one its stored Swapping condition names
		mode = instances.ConditionOf(s.GetConditions(), CondSwapping).GetReason()
	}
	switch s.GetState() {
	case v1.SlotState_SLOT_STATE_READY:
		set(CondReady, true, "OccupantReady", "")
		s.ObservedGeneration = gen
	case v1.SlotState_SLOT_STATE_SWAPPING:
		if mode == ModeDrainFirst {
			set(CondReady, false, "SwapDraining", "")
		} else {
			set(CondReady, true, "OldOccupantServing", "")
		}
	case v1.SlotState_SLOT_STATE_STARTING:
		set(CondReady, false, "OccupantStarting", "")
	case v1.SlotState_SLOT_STATE_IDLE:
		set(CondReady, false, "OccupantIdle", "")
	case v1.SlotState_SLOT_STATE_DRAINING:
		set(CondReady, false, "OccupantDraining", "")
	case v1.SlotState_SLOT_STATE_FAILED:
		set(CondReady, false, "OccupantFailed", s.GetError())
	default:
		set(CondReady, false, "Empty", "")
	}
	if s.GetState() == v1.SlotState_SLOT_STATE_SWAPPING {
		set(CondSwapping, true, mode, "")
	} else {
		set(CondSwapping, false, "Settled", s.GetError())
	}
	if p, ok := m.phases[s.GetId()]; ok {
		set(CondCanary, p.active, p.reason, p.message)
	}
}

// Resolves a slot into planning constraints, implementing the instance manager's slots
func (m *Manager) Reservation(ctx context.Context, id string) (*instances.Reservation, error) {
	s, err := m.find(id)
	if err != nil {
		return nil, err
	}
	res := &instances.Reservation{
		SlotID:      s.GetId(),
		Name:        s.GetName(),
		DeviceIDs:   m.localDevices(s.GetDeviceIds()),
		MemoryBytes: s.GetMemoryBytes(),
		PoolBytes:   m.localPools(s.GetPoolBytes()),
		Placement:   s.GetPlacement(),
		RuntimeID:   s.GetRuntimeId(),
		Params:      s.GetParams(),
		InstanceID:  s.GetInstanceId(),
		Args:        s.GetArgs(),
	}
	if f := m.liveFormation(s); f != nil {
		res.FormationID, res.FormationName = f.GetId(), f.GetName()
	}
	return res, nil
}

// The slot's device ids as this node's prober names them, a pin naming this node standing for the device itself
func (m *Manager) localDevices(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		node, device := m.splitDevice(id)
		if node == "" || m.Formations != nil && node == m.Formations.Self() {
			out = append(out, device)
			continue
		}
		out = append(out, id)
	}
	return out
}

// The slot's pool caps this node holds keyed by pool id alone, caps on other nodes left out
func (m *Manager) localPools(caps map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(caps))
	for key, bytes := range caps {
		node, pool := m.splitDevice(key)
		if node == "" || m.Formations != nil && node == m.Formations.Self() {
			out[pool] = bytes
		}
	}
	return out
}

// Requires a nonempty name unused by other slots, their aliases, or routes another owner named
func (m *Manager) checkName(name, self string) error {
	if name == "" {
		return fmt.Errorf("%w: name required", ErrSlot)
	}
	if strings.ContainsAny(name, " \t\n/") {
		return fmt.Errorf("%w: a slot name has no spaces or slashes", ErrSlot)
	}
	if s, err := m.find(name); err == nil && s.GetId() != self {
		return fmt.Errorf("%w: slot %q exists", ErrSlot, name)
	}
	if owner := m.aliasOwner(name); owner != nil && owner.GetId() != self {
		return fmt.Errorf("%w: %q is an alias of slot %s", ErrSlot, name, owner.GetName())
	}
	if r, taken := m.Routes.Route(name); taken && (self == "" || r.GetOwnerKind() != pools.OwnerSlot || r.GetOwnerId() != self) {
		return fmt.Errorf("%w: %q is already a route, pick another name", ErrSlot, name)
	}
	return nil
}

// The slot with the name among its aliases, nil when none has it
func (m *Manager) aliasOwner(name string) *v1.Slot {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.slots {
		for _, a := range s.GetAliases() {
			if a.GetName() == name {
				return proto.Clone(s).(*v1.Slot)
			}
		}
	}
	return nil
}

// Refuses a profile that offers mutating tools with no tool server to take them from
func checkProfile(p *v1.Profile) error {
	if p.GetMutatingTools() && len(p.GetToolServers()) == 0 {
		return errors.New("mutating tools need at least one tool server")
	}
	return nil
}

// Trims and checks a slot's alias list against its own name, each inherit naming another slot stored by id
func (m *Manager) checkAliases(list []*v1.SlotAlias, self, primary string) ([]*v1.SlotAlias, error) {
	out := make([]*v1.SlotAlias, 0, len(list))
	seen := map[string]bool{}
	for _, a := range list {
		name := strings.TrimSpace(a.GetName())
		if name == primary {
			return nil, fmt.Errorf("%w: alias %q is the slot's own name", ErrSlot, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("%w: alias %q is listed twice", ErrSlot, name)
		}
		if err := m.checkName(name, self); err != nil {
			return nil, err
		}
		seen[name] = true
		clean := &v1.SlotAlias{Name: name}
		if ref := strings.TrimSpace(a.GetInheritSlotId()); ref != "" {
			from, err := m.find(ref)
			switch {
			case ref == primary || err == nil && from.GetId() == self:
				return nil, fmt.Errorf("%w: alias %q cannot inherit from its own slot, leave the inherit empty for that", ErrSlot, name)
			case err != nil:
				return nil, fmt.Errorf("%w: alias %q inherits from an %v", ErrSlot, name, err)
			}
			clean.InheritSlotId = from.GetId()
		}
		out = append(out, clean)
	}
	return out, nil
}

// Trims a slot's extra words and refuses ones that would not render at launch
func checkArgs(args string) (string, error) {
	args = strings.TrimSpace(args)
	if err := runtimes.CheckArgs(args); err != nil {
		return "", fmt.Errorf("%w: %v", ErrSlot, err)
	}
	return args, nil
}

// Every public name of the slot, its own then its aliases in order
func names(s *v1.Slot) []string {
	out := []string{s.GetName()}
	for _, a := range s.GetAliases() {
		out = append(out, a.GetName())
	}
	return out
}

// Limits and profile for one of the slot's names, an alias taking those of the slot it inherits
func (m *Manager) settingsFor(s *v1.Slot, name string) (*v1.Policy, *v1.Profile, error) {
	for _, a := range s.GetAliases() {
		if a.GetName() != name || a.GetInheritSlotId() == "" {
			continue
		}
		m.mu.Lock()
		from, held := m.slots[a.GetInheritSlotId()]
		if held {
			from = proto.Clone(from).(*v1.Slot)
		}
		m.mu.Unlock()
		if !held {
			return nil, nil, fmt.Errorf("alias %s inherits from slot %s, which is gone", name, a.GetInheritSlotId())
		}
		return from.GetPolicy(), from.GetProfile(), nil
	}
	return s.GetPolicy(), s.GetProfile(), nil
}

// The other slots with an alias inheriting from the slot
func (m *Manager) heirsOf(id string) []*v1.Slot {
	var out []*v1.Slot
	for _, s := range m.List() {
		if s.GetId() != id && slices.ContainsFunc(s.GetAliases(), func(a *v1.SlotAlias) bool { return a.GetInheritSlotId() == id }) {
			out = append(out, s)
		}
	}
	return out
}

// Clears the inherit of every alias naming a slot no longer held, reporting a change, the caller holding the lock
func (m *Manager) unheritLocked(s *v1.Slot) bool {
	changed := false
	for _, a := range s.GetAliases() {
		if _, held := m.slots[a.GetInheritSlotId()]; a.GetInheritSlotId() != "" && !held {
			a.InheritSlotId = ""
			changed = true
		}
	}
	return changed
}

// Names the routes of every occupied slot in the list again, so its aliases take what they inherit now
func (m *Manager) reclaimAll(ctx context.Context, list []*v1.Slot) error {
	var errs []error
	for _, s := range list {
		if occ, ok := m.occupantOf(s); ok {
			if err := m.claimRoutes(ctx, s, occ); err != nil {
				errs = append(errs, fmt.Errorf("slot %s: %w", s.GetName(), err))
			}
		}
	}
	return errors.Join(errs...)
}

// Slot device pins
func devicesFor(placement v1.Placement, ids []string) []string {
	if placement == v1.Placement_PLACEMENT_HOST {
		return nil
	}
	return ids
}

// Creates a slot, checking that its devices, its capped pools and the slots its aliases inherit from exist
func (m *Manager) Create(ctx context.Context, req *v1.CreateSlotRequest) (*v1.Slot, error) {
	name := strings.TrimSpace(req.GetName())
	if err := m.checkName(name, ""); err != nil {
		return nil, err
	}
	if err := checkProfile(req.GetProfile()); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSlot, err)
	}
	aliases, err := m.checkAliases(req.GetAliases(), "", name)
	if err != nil {
		return nil, err
	}
	args, err := checkArgs(req.GetArgs())
	if err != nil {
		return nil, err
	}
	devices := devicesFor(req.GetPlacement(), req.GetDeviceIds())
	if err := m.checkDevices(ctx, devices); err != nil {
		return nil, err
	}
	caps, err := m.checkPoolBytes(ctx, req.GetPoolBytes())
	if err != nil {
		return nil, err
	}
	now := timestamppb.Now()
	s := &v1.Slot{
		Id:          db.NewID(),
		Name:        name,
		Description: strings.TrimSpace(req.GetDescription()),
		Aliases:     aliases,
		Placement:   req.GetPlacement(),
		DeviceIds:   devices,
		MemoryBytes: req.GetMemoryBytes(),
		PoolBytes:   caps,
		RuntimeId:   req.GetRuntimeId(),
		Params:      req.GetParams(),
		Args:        args,
		Policy:      req.GetPolicy(),
		Profile:     req.GetProfile(),
		State:       v1.SlotState_SLOT_STATE_EMPTY,
		CreatedAt:   now,
		UpdatedAt:   now,
		Meta:        &v1.ObjectMeta{Generation: 1, CreatedAt: now, UpdatedAt: now},
	}
	m.mu.Lock()
	if m.slots == nil {
		m.slots = map[string]*v1.Slot{}
	}
	s.Position = uint32(len(m.slots) + 1)
	m.unheritLocked(s)
	m.statusLocked(s)
	m.slots[s.GetId()] = s
	snapshot := proto.Clone(s).(*v1.Slot)
	m.mu.Unlock()
	if err := m.DB.PutSlot(ctx, snapshot); err != nil {
		m.mu.Lock()
		delete(m.slots, s.GetId())
		m.mu.Unlock()
		return nil, err
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_SLOT, v1.EventAction_EVENT_ACTION_CREATED, s.GetId(), snapshot)
	if req.GetPosition() > 0 {
		m.place(s.GetId(), req.GetPosition())
	}
	return m.find(s.GetId())
}

// Checks every cap is above zero and names a pool its node holds, keying named nodes by id
func (m *Manager) checkPoolBytes(ctx context.Context, caps map[string]uint64) (map[string]uint64, error) {
	if len(caps) == 0 {
		return nil, nil
	}
	local, err := m.Host.Profile(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make(map[string]uint64, len(caps))
	// The pools capped so far, a pool on this node counted once however its key names it
	held := map[string]string{}
	for key, bytes := range caps {
		if bytes == 0 {
			return nil, fmt.Errorf("%w: pool %q needs a cap above zero, leave it out to lift its cap", ErrSlot, key)
		}
		profile, node, pool := local, "", key
		if ref, rest, named := strings.Cut(key, "/"); named {
			if m.Formations == nil {
				return nil, fmt.Errorf("%w: pool %q names node %q, but this daemon is in no mesh", ErrSlot, key, ref)
			}
			node, pool = ref, rest
			if ref != m.Formations.Self() {
				rec, err := m.Formations.Node(ref)
				if err != nil {
					return nil, fmt.Errorf("%w: pool %q names a node this mesh does not have: %v", ErrSlot, key, err)
				}
				node = rec.GetId()
				if node != m.Formations.Self() {
					profile = rec.GetProfile()
				}
			}
		}
		if !slices.ContainsFunc(profile.GetPools(), func(p *v1.MemoryPool) bool { return p.GetId() == pool }) {
			return nil, fmt.Errorf("%w: pool %q was not probed on its node, see nebu host or nebu mesh nodes", ErrSlot, key)
		}
		at, stored := pool, pool
		if node != "" {
			stored = node + "/" + pool
			if node != m.Formations.Self() {
				at = stored
			}
		}
		if first, twice := held[at]; twice {
			return nil, fmt.Errorf("%w: pools %q and %q cap the same pool", ErrSlot, first, key)
		}
		held[at] = key
		out[stored] = bytes
	}
	return out, nil
}

func (m *Manager) checkDevices(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	profile, err := m.Host.Profile(ctx, false)
	if err != nil {
		return err
	}
	for _, id := range ids {
		node, device := m.splitDevice(id)
		held := profile
		if node != "" && m.Formations != nil && node != m.Formations.Self() {
			rec, err := m.Formations.Node(node)
			if err != nil {
				return fmt.Errorf("%w: device %q names a node this mesh does not have: %v", ErrSlot, id, err)
			}
			held = rec.GetProfile()
			if host.Device(held, device) == nil {
				return fmt.Errorf("%w: %s has no device %q, see nebu mesh nodes", ErrSlot, rec.GetName(), device)
			}
		} else if host.Device(held, device) == nil {
			return fmt.Errorf("%w: device %q was not probed, see nebu host", ErrSlot, id)
		}
		if d := host.Device(held, device); !host.OwnMemory(held, d) {
			return fmt.Errorf("%w: device %q (%s) has no memory of its own, so a slot cannot pin it", ErrSlot, id, d.GetName())
		}
	}
	return nil
}

// Updates names, description and route limits at once, aliases inheriting them too, other settings applying on the next run
func (m *Manager) Update(ctx context.Context, req *v1.UpdateSlotRequest) (*v1.Slot, error) {
	s, err := m.find(req.GetId())
	if err != nil {
		return nil, err
	}
	devices := devicesFor(req.GetPlacement(), req.GetDeviceIds())
	if err := m.checkDevices(ctx, devices); err != nil {
		return nil, err
	}
	caps, err := m.checkPoolBytes(ctx, req.GetPoolBytes())
	if err != nil {
		return nil, err
	}
	if err := checkProfile(req.GetProfile()); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSlot, err)
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		name = s.GetName()
	}
	aliases, err := m.checkAliases(req.GetAliases(), s.GetId(), name)
	if err != nil {
		return nil, err
	}
	args, err := checkArgs(req.GetArgs())
	if err != nil {
		return nil, err
	}
	if name != s.GetName() {
		if err := m.checkName(name, s.GetId()); err != nil {
			return nil, err
		}
		if s.GetState() == v1.SlotState_SLOT_STATE_SWAPPING {
			return nil, fmt.Errorf("%w: slot %s is swapping, rename it once that settles", ErrSlot, s.GetName())
		}
	}
	next := m.update(s.GetId(), func(sl *v1.Slot) {
		sl.Name = name
		sl.Description = strings.TrimSpace(req.GetDescription())
		sl.Aliases = aliases
		m.unheritLocked(sl)
		sl.Placement = req.GetPlacement()
		sl.DeviceIds = devices
		sl.MemoryBytes = req.GetMemoryBytes()
		sl.PoolBytes = caps
		sl.RuntimeId = req.GetRuntimeId()
		sl.Params = req.GetParams()
		sl.Args = args
		sl.Policy = req.GetPolicy()
		sl.Profile = req.GetProfile()
		if sl.Request != nil {
			sl.Request.Name = name
		}
		bump(sl)
	})
	if req.GetPosition() > 0 && req.GetPosition() != next.GetPosition() {
		m.place(next.GetId(), req.GetPosition())
		next = m.mustFind(next.GetId())
	}
	m.dropNames(ctx, s, names(next))
	var errs []error
	if occ, ok := m.occupantOf(next); ok {
		if err := m.claimRoutes(ctx, next, occ); err != nil {
			errs = append(errs, err)
		}
	}
	if !proto.Equal(s.GetPolicy(), next.GetPolicy()) || !proto.Equal(s.GetProfile(), next.GetProfile()) {
		errs = append(errs, m.reclaimAll(ctx, m.heirsOf(next.GetId())))
	}
	if err := errors.Join(errs...); err != nil {
		return next, fmt.Errorf("%w: %v", ErrSlot, err)
	}
	return next, nil
}

func aliasNames(list []*v1.SlotAlias) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.GetName())
	}
	return out
}

// Drops the routes of names the slot answered to before and no longer does
func (m *Manager) dropNames(ctx context.Context, before *v1.Slot, keep []string) {
	for _, name := range names(before) {
		if !slices.Contains(keep, name) {
			m.Registrar.DropRoute(ctx, ownerOf(before.GetId()), name)
		}
	}
}

// Adds an alias to a slot or updates the slot it inherits, returning its route once an occupant serves
func (m *Manager) AddAlias(ctx context.Context, id string, alias *v1.SlotAlias) (*v1.Slot, *v1.Route, error) {
	s, err := m.find(id)
	if err != nil {
		return nil, nil, err
	}
	checked, err := m.checkAliases([]*v1.SlotAlias{alias}, s.GetId(), s.GetName())
	if err != nil {
		return nil, nil, err
	}
	clean := checked[0]
	next := m.update(s.GetId(), func(sl *v1.Slot) {
		bump(sl)
		if i := slices.IndexFunc(sl.Aliases, func(a *v1.SlotAlias) bool { return a.GetName() == clean.GetName() }); i >= 0 {
			sl.Aliases[i] = clean
		} else {
			sl.Aliases = append(sl.Aliases, clean)
		}
		m.unheritLocked(sl)
	})
	occ, ok := m.occupantOf(next)
	if !ok {
		return next, nil, nil
	}
	if err := m.claimRoutes(ctx, next, occ); err != nil {
		return next, nil, fmt.Errorf("%w: %v", ErrSlot, err)
	}
	route, _ := m.Routes.Route(clean.GetName())
	return next, route, nil
}

// Removes an alias from its slot and returns the route it answered as
func (m *Manager) RemoveAlias(ctx context.Context, id, name string) (*v1.Slot, *v1.Route, error) {
	s, err := m.find(id)
	if err != nil {
		return nil, nil, err
	}
	if name == s.GetName() {
		return nil, nil, fmt.Errorf("%w: %q is the name of slot %s, delete the slot instead", ErrSlot, name, s.GetName())
	}
	if !slices.Contains(aliasNames(s.GetAliases()), name) {
		return nil, nil, fmt.Errorf("%w: slot %s has no alias %q", ErrSlot, s.GetName(), name)
	}
	route, named := m.Routes.Route(name)
	next := m.update(s.GetId(), func(sl *v1.Slot) {
		bump(sl)
		sl.Aliases = slices.DeleteFunc(sl.Aliases, func(a *v1.SlotAlias) bool { return a.GetName() == name })
	})
	m.Registrar.DropRoute(ctx, ownerOf(next.GetId()), name)
	if !named {
		route = &v1.Route{Name: name, OwnerKind: pools.OwnerSlot, OwnerId: next.GetId()}
	}
	return next, route, nil
}

// Deletes a slot, stopping its occupant when forced, and clears every alias inheriting from it
func (m *Manager) Delete(ctx context.Context, id string, force bool) (*v1.Slot, error) {
	s, err := m.find(id)
	if err != nil {
		return nil, err
	}
	if err := m.claim(s.GetId()); err != nil {
		return nil, err
	}
	defer m.release(s.GetId())
	if live := m.liveInstance(s); live != nil && !force {
		return nil, fmt.Errorf("%w: slot %s serves %s, evict it or pass --force", ErrSlot, s.GetName(), live.GetName())
	}
	if f := m.liveFormation(s); f != nil && !force {
		return nil, fmt.Errorf("%w: slot %s serves formation %s, evict it or pass --force", ErrSlot, s.GetName(), f.GetName())
	}
	m.update(s.GetId(), func(sl *v1.Slot) { sl.DesiredRunning = false })
	if err := m.retireAll(ctx, s); err != nil {
		return nil, err
	}
	if _, err := m.DB.DeleteSlot(ctx, s.GetId()); err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.slots, s.GetId())
	delete(m.modes, s.GetId())
	delete(m.phases, s.GetId())
	var heirs []*v1.Slot
	for _, sl := range m.slots {
		if m.unheritLocked(sl) {
			heirs = append(heirs, m.bumpLocked(sl))
		}
	}
	m.mu.Unlock()
	sortSlots(heirs)
	m.persist(heirs)
	m.dropRoutes(ctx, s)
	m.Events.Publish(v1.EventKind_EVENT_KIND_SLOT, v1.EventAction_EVENT_ACTION_DELETED, s.GetId(), s)
	m.renumber()
	if err := m.reclaimAll(ctx, heirs); err != nil {
		return s, fmt.Errorf("%w: slot %s is deleted, and routes of slots that inherited from it were not named again: %v", ErrSlot, s.GetName(), err)
	}
	return s, nil
}

// Stops the occupant and clears the saved request and the desire to run
func (m *Manager) Evict(ctx context.Context, id string) (*v1.Slot, error) {
	s, err := m.find(id)
	if err != nil {
		return nil, err
	}
	if err := m.claim(s.GetId()); err != nil {
		return nil, err
	}
	defer m.release(s.GetId())
	m.update(s.GetId(), func(sl *v1.Slot) { sl.DesiredRunning = false })
	if err := m.retireAll(ctx, s); err != nil {
		return nil, err
	}
	m.dropRoutes(ctx, s)
	return m.update(s.GetId(), func(sl *v1.Slot) {
		sl.InstanceId, sl.FormationId, sl.State, sl.Error, sl.TaskId, sl.Request = "", "", v1.SlotState_SLOT_STATE_EMPTY, "", "", nil
	}), nil
}

// Relaunches the slot's saved request and wants it running
func (m *Manager) Relaunch(ctx context.Context, id string) (*v1.Slot, *v1.Instance, *v1.Task, error) {
	s, err := m.find(id)
	if err != nil {
		return nil, nil, nil, err
	}
	if s.GetRequest() == nil {
		return nil, nil, nil, fmt.Errorf("%w: slot %s has nothing to relaunch, run a model in it", ErrSlot, s.GetName())
	}
	if err := m.claim(s.GetId()); err != nil {
		return nil, nil, nil, err
	}
	defer m.release(s.GetId())
	if live := m.liveInstance(s); live != nil {
		return nil, nil, nil, fmt.Errorf("%w: slot %s serves %s, swap or evict instead", ErrSlot, s.GetName(), live.GetName())
	}
	if f := m.liveFormation(s); f != nil {
		return nil, nil, nil, fmt.Errorf("%w: slot %s serves formation %s, swap or evict instead", ErrSlot, s.GetName(), f.GetName())
	}
	run := proto.Clone(s.GetRequest()).(*v1.RunRequest)
	run.SlotId, run.Name = s.GetId(), s.GetName()
	if m.meshRun(s, run) {
		_, task, err := m.launchFormation(ctx, s, run)
		if err != nil {
			return nil, nil, nil, err
		}
		return m.mustFind(s.GetId()), nil, task, nil
	}
	in, task, err := m.Instances.Run(ctx, run)
	if err != nil {
		m.update(s.GetId(), func(sl *v1.Slot) {
			sl.InstanceId, sl.State, sl.Error = "", v1.SlotState_SLOT_STATE_FAILED, err.Error()
		})
		return nil, nil, nil, err
	}
	next := m.update(s.GetId(), func(sl *v1.Slot) {
		sl.InstanceId, sl.State, sl.Error, sl.TaskId, sl.Request, sl.DesiredRunning = in.GetId(), v1.SlotState_SLOT_STATE_STARTING, "", task.GetId(), in.GetRequest(), true
	})
	if err := m.claimRoutes(ctx, next, occupant{in: in}); err != nil {
		m.Log.Warn("slot routes not named", "slot", next.GetName(), "err", err)
	}
	return next, in, task, nil
}

// One instance or one formation a slot serves
type occupant struct {
	in *v1.Instance
	f  *v1.Formation
}

// The slot's live occupant, false when it serves nothing
func (m *Manager) occupantOf(s *v1.Slot) (occupant, bool) {
	if f := m.liveFormation(s); f != nil {
		return occupant{f: f}, true
	}
	if in := m.liveInstance(s); in != nil {
		return occupant{in: in}, true
	}
	return occupant{}, false
}

// The occupant's model as a pool key
func (o occupant) model() pools.ModelKey {
	if o.f != nil {
		return pools.ModelKey{SourceID: o.f.GetSourceId(), Repo: o.f.GetRepo(), Group: o.f.GetGroup()}
	}
	return pools.ModelKey{SourceID: o.in.GetSourceId(), Repo: o.in.GetRepo(), Group: o.in.GetGroup()}
}

func (o occupant) runtime() string {
	if o.f != nil {
		return o.f.GetRuntimeId()
	}
	return o.in.GetRuntimeId()
}

// The model name the occupant's endpoints answer to
func (o occupant) served() string {
	if o.f != nil {
		return o.f.GetName()
	}
	return instances.ServedName(o.in)
}

// The occupant as one weighted target of a split route, its pool named with the accelerator its plan runs on
func (m *Manager) target(ctx context.Context, o occupant, weight uint32) pools.WeightedTarget {
	return pools.WeightedTarget{Pool: pools.PoolIDOf(o.model(), o.runtime(), m.acceleratorOf(ctx, o)), Model: o.served(), Weight: weight}
}

// The accelerator class the occupant's plan runs on, empty for a formation or an occupant without a plan
func (m *Manager) acceleratorOf(ctx context.Context, o occupant) string {
	if o.f != nil {
		return ""
	}
	return m.Instances.AcceleratorOf(ctx, o.in)
}

func (o occupant) name() string {
	if o.f != nil {
		return "formation " + o.f.GetName()
	}
	return o.in.GetName()
}

// The pool membership the slot declares for its occupant, from the occupant's recorded or requested params
func (m *Manager) membershipOf(o occupant) (pools.Membership, error) {
	rt, err := m.Instances.Runtimes.Get(o.runtime())
	if err != nil {
		return pools.Membership{}, err
	}
	rec := &v1.Instance{SourceId: o.model().SourceID, Repo: o.model().Repo, Group: o.model().Group, RuntimeId: o.runtime()}
	schedulerConfig := ""
	switch {
	case o.f != nil:
		rec.Params = o.f.GetRequest().GetParams()
		schedulerConfig = o.f.GetPlan().GetSchedulerConfig()
	case len(o.in.GetParams()) > 0:
		rec.Params, rec.Plan = o.in.GetParams(), o.in.GetPlan()
	default:
		rec.Params, rec.Plan = o.in.GetRequest().GetParams(), o.in.GetPlan()
	}
	return instances.MembershipOf(rt, rec, schedulerConfig, m.acceleratorOf(context.Background(), o))
}

// Declares the slot's pool after its occupant and names every one of its names at the occupant's served name
func (m *Manager) claimRoutes(ctx context.Context, s *v1.Slot, o occupant) error {
	membership, err := m.membershipOf(o)
	if err != nil {
		return err
	}
	owner := ownerOf(s.GetId())
	if err := m.Registrar.Publish(ctx, owner, membership, nil); err != nil {
		return fmt.Errorf("pool of slot %s: %w", s.GetName(), err)
	}
	for _, name := range names(s) {
		policy, profile, err := m.settingsFor(s, name)
		if err != nil {
			return fmt.Errorf("route %s: %w", name, err)
		}
		if _, err := m.Registrar.NameRoute(ctx, owner, name, pools.RouteSpec{Policy: policy, Profile: profile, ServedName: o.served(), Shape: o.f.GetShape(), Seats: pools.SeatIDs(o.f)}); err != nil {
			return fmt.Errorf("route %s: %w", name, err)
		}
	}
	return nil
}

// Splits every name of the slot over weighted occupants
func (m *Manager) splitRoutes(ctx context.Context, s *v1.Slot, targets []pools.WeightedTarget) error {
	for _, name := range names(s) {
		if err := m.Registrar.SplitRoute(ctx, ownerOf(s.GetId()), name, targets); err != nil {
			return fmt.Errorf("route %s: %w", name, err)
		}
	}
	return nil
}

// Drops every route and the pool claim of the slot
func (m *Manager) dropRoutes(ctx context.Context, s *v1.Slot) {
	m.Registrar.DropRoutes(ctx, ownerOf(s.GetId()))
	m.Registrar.Unpublish(ctx, ownerOf(s.GetId()))
}

// Stops an occupant through its drain-first stop path
func (m *Manager) retire(ctx context.Context, o occupant) error {
	if o.f != nil {
		_, err := m.Formations.Stop(ctx, o.f.GetId())
		return err
	}
	_, err := m.Instances.Stop(ctx, o.in.GetId())
	return err
}

// Swaps the model while keeping the slot's names, blue-green, drain first, or a canary shifting traffic in steps
func (m *Manager) Swap(ctx context.Context, req *v1.SwapRequest) (*v1.Slot, *v1.Instance, *v1.Task, error) {
	s, err := m.find(req.GetSlotId())
	if err != nil {
		return nil, nil, nil, err
	}
	run := proto.Clone(req.GetRun()).(*v1.RunRequest)
	if run == nil || run.GetRepo() == "" {
		return nil, nil, nil, fmt.Errorf("%w: swap needs a model to run", ErrSlot)
	}
	plan, canary, err := canaryOf(req)
	switch {
	case err != nil:
		return nil, nil, nil, err
	case canary && req.GetDrainFirst():
		return nil, nil, nil, fmt.Errorf("%w: a canary runs beside the occupant and cannot drain it first", ErrSlot)
	case canary && m.Outcomes == nil:
		return nil, nil, nil, fmt.Errorf("%w: this daemon reads no request outcomes to judge a canary by", ErrSlot)
	}
	run.SlotId, run.Name = s.GetId(), s.GetName()
	// Hold the claim until starting or swapping to prevent concurrent swaps
	if err := m.claim(s.GetId()); err != nil {
		return nil, nil, nil, err
	}
	defer m.release(s.GetId())
	mesh := m.meshRun(s, run)
	verdict, detail, err := m.verdict(ctx, s, run, mesh)
	if err != nil {
		return nil, nil, nil, err
	}
	old, occupied := m.occupantOf(s)
	if !occupied {
		if canary {
			return nil, nil, nil, fmt.Errorf("%w: slot %s serves nothing for a canary to be compared against", ErrSlot, s.GetName())
		}
		return m.fill(ctx, s, run, mesh)
	}
	mode := ModeBlueGreen
	switch {
	case canary && verdict != v1.FitVerdict_FIT_VERDICT_FITS:
		return nil, nil, nil, fmt.Errorf("%w: a canary runs beside %s and the plan says both do not fit: %s", ErrSlot, old.name(), detail)
	case canary:
		mode = ModeCanary
	case req.GetDrainFirst() || verdict != v1.FitVerdict_FIT_VERDICT_FITS:
		mode = ModeDrainFirst
	}
	var gen uint64
	m.mu.Lock()
	if m.modes == nil {
		m.modes = map[string]string{}
	}
	m.modes[s.GetId()] = mode
	m.mu.Unlock()
	s = m.update(s.GetId(), func(sl *v1.Slot) {
		sl.State, sl.Error, sl.DesiredRunning = v1.SlotState_SLOT_STATE_SWAPPING, "", true
		bump(sl)
		gen = sl.GetMeta().GetGeneration()
		if canary {
			m.phaseLocked(sl.GetId(), phase{active: true, reason: CanaryLaunching, message: "starting the canary beside " + old.name()})
		} else {
			m.clearPhaseLocked(sl)
		}
	})
	if canary {
		run.Name = fmt.Sprintf("%s~%d", s.GetName(), gen)
	}
	title := fmt.Sprintf("swap %s to %s %s", s.GetName(), run.GetRepo(), run.GetGroup())
	task := m.Tasks.Start(kindSwap, title, map[string]string{"slot": s.GetId(), "name": s.GetName()}, func(ctx context.Context, h *tasks.Handle) error {
		h.Logf("swap mode %s, plan %s: %s", mode, text.Enum(verdict), detail)
		defer func() {
			m.mu.Lock()
			delete(m.modes, s.GetId())
			m.mu.Unlock()
		}()
		switch mode {
		case ModeDrainFirst:
			return m.swapDrainFirst(ctx, h, s, old, run, mesh)
		case ModeCanary:
			return m.swapCanary(ctx, h, s, old, run, mesh, plan)
		}
		return m.swapBlueGreen(ctx, h, s, old, run, mesh)
	})
	m.update(s.GetId(), func(sl *v1.Slot) { sl.TaskId = task.GetId() })
	return m.mustFind(s.GetId()), old.in, task, nil
}

// The plan verdict of a run in the slot, by the conductor when it spans the mesh
func (m *Manager) verdict(ctx context.Context, s *v1.Slot, run *v1.RunRequest, mesh bool) (v1.FitVerdict, string, error) {
	if mesh {
		plan, _, err := m.Formations.Plan(ctx, m.meshRequest(s, run))
		return plan.GetVerdict(), plan.GetDetail(), err
	}
	plan, err := m.Instances.Plan(ctx, run)
	return plan.GetVerdict(), plan.GetDetail(), err
}

// Launches a run into an empty slot and wants it running, forgetting an earlier canary's condition
func (m *Manager) fill(ctx context.Context, s *v1.Slot, run *v1.RunRequest, mesh bool) (*v1.Slot, *v1.Instance, *v1.Task, error) {
	m.update(s.GetId(), m.clearPhaseLocked)
	if mesh {
		_, task, err := m.launchFormation(ctx, s, run)
		if err != nil {
			return nil, nil, nil, err
		}
		return m.mustFind(s.GetId()), nil, task, nil
	}
	in, task, err := m.Instances.Run(ctx, run)
	if err != nil {
		return nil, nil, nil, err
	}
	next := m.update(s.GetId(), func(sl *v1.Slot) {
		sl.InstanceId, sl.FormationId, sl.State, sl.Error, sl.TaskId, sl.Request, sl.DesiredRunning = in.GetId(), "", v1.SlotState_SLOT_STATE_STARTING, "", task.GetId(), in.GetRequest(), true
		bump(sl)
	})
	if err := m.claimRoutes(ctx, next, occupant{in: in}); err != nil {
		m.Log.Warn("slot routes not named", "slot", next.GetName(), "err", err)
	}
	return next, in, task, nil
}

// Launches the slot's next occupant and waits for it to be ready
func (m *Manager) launchFresh(ctx context.Context, h *tasks.Handle, s *v1.Slot, run *v1.RunRequest, mesh, swap bool) (occupant, error) {
	if mesh {
		f, err := m.formationReady(ctx, h, s, run, swap)
		return occupant{f: f}, err
	}
	in, err := m.launch(ctx, h, run, swap)
	return occupant{in: in}, err
}

// Launches the slot's model and returns the ready instance
func (m *Manager) launch(ctx context.Context, h *tasks.Handle, run *v1.RunRequest, swap bool) (*v1.Instance, error) {
	if swap {
		ctx = instances.WithSwap(ctx)
	}
	in, task, err := m.Instances.Run(ctx, run)
	if err != nil {
		return nil, err
	}
	h.Logf("starting %s as instance %s", run.GetRepo(), in.GetId())
	if err := m.Tasks.WaitOK(ctx, task.GetId()); err != nil {
		return in, err
	}
	return m.Instances.Get(in.GetId())
}

// Makes an occupant the slot's own, its request saved and every name pointed at it
func (m *Manager) occupy(ctx context.Context, s *v1.Slot, o occupant) error {
	next := m.update(s.GetId(), func(sl *v1.Slot) {
		if o.f != nil {
			sl.FormationId, sl.InstanceId, sl.Request = o.f.GetId(), "", o.f.GetRequest()
		} else {
			sl.FormationId, sl.InstanceId, sl.Request = "", o.in.GetId(), o.in.GetRequest()
		}
	})
	return m.claimRoutes(ctx, next, o)
}

// Settles a swap on whatever serves, ready with it or failed with nothing, the error kept on the slot
func (m *Manager) settle(ctx context.Context, s *v1.Slot, serving *occupant, note string) {
	m.mu.Lock()
	delete(m.modes, s.GetId())
	m.mu.Unlock()
	if serving == nil {
		m.update(s.GetId(), func(sl *v1.Slot) {
			sl.InstanceId, sl.FormationId, sl.State, sl.Error = "", "", v1.SlotState_SLOT_STATE_FAILED, note
		})
		m.Registrar.Unpublish(ctx, ownerOf(s.GetId()))
		return
	}
	if err := m.occupy(ctx, s, *serving); err != nil {
		note = strings.TrimSpace(note + "; routes: " + err.Error())
	}
	m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_READY, note })
}

// Starts the replacement beside the occupant, points every name at it, then retires the old through its drain
func (m *Manager) swapBlueGreen(ctx context.Context, h *tasks.Handle, s *v1.Slot, old occupant, run *v1.RunRequest, mesh bool) error {
	h.Progress(0, 3, "starting "+run.GetRepo())
	fresh, err := m.launchFresh(ctx, h, s, run, mesh, true)
	if err != nil {
		m.stopFailed(h, fresh)
		m.settle(ctx, s, &old, fmt.Sprintf("new occupant failed, %s still serving: %v", old.name(), err))
		return err
	}
	h.Progress(1, 3, "switching routes")
	if err := m.occupy(ctx, s, fresh); err != nil {
		h.Logf("routes not pointed at %s: %v", fresh.name(), err)
	}
	h.Logf("route %s now serves %s", s.GetName(), fresh.name())
	h.Progress(2, 3, "draining "+old.name())
	retireErr := m.retire(ctx, old)
	note := ""
	if retireErr != nil {
		note = fmt.Sprintf("the old occupant %s did not stop: %v", old.name(), retireErr)
		h.Logf("%s", note)
	}
	m.settle(ctx, s, &fresh, note)
	h.Progress(3, 3, "ready")
	return retireErr
}

// Stops a failed replacement so it holds no memory, logging when the stop fails too
func (m *Manager) stopFailed(h *tasks.Handle, o occupant) {
	if o.in == nil && o.f == nil {
		return
	}
	if err := m.retire(context.Background(), o); err != nil {
		h.Logf("the failed %s did not stop either: %v", o.name(), err)
	}
}

// Drains and stops the occupant before launching, bringing the previous request back when the launch fails
func (m *Manager) swapDrainFirst(ctx context.Context, h *tasks.Handle, s *v1.Slot, old occupant, run *v1.RunRequest, mesh bool) error {
	h.Progress(0, 3, "draining "+old.name())
	if err := m.retire(ctx, old); err != nil {
		m.settle(ctx, s, nil, err.Error())
		return err
	}
	h.Logf("stopped %s", old.name())
	h.Progress(1, 3, "starting "+run.GetRepo())
	fresh, err := m.launchFresh(ctx, h, s, run, mesh, false)
	if err == nil {
		h.Progress(2, 3, "switching routes")
		m.settle(ctx, s, &fresh, "")
		h.Progress(3, 3, "ready")
		return nil
	}
	h.Logf("new occupant failed: %v", err)
	m.stopFailed(h, fresh)
	previous := s.GetRequest()
	if previous == nil {
		m.settle(ctx, s, nil, err.Error())
		return err
	}
	h.Progress(2, 3, "rolling back to "+previous.GetRepo())
	previous = proto.Clone(previous).(*v1.RunRequest)
	previous.SlotId, previous.Name = s.GetId(), s.GetName()
	restored, berr := m.launchFresh(ctx, h, s, previous, m.meshRun(s, previous), false)
	if berr != nil {
		m.stopFailed(h, restored)
		m.settle(ctx, s, nil, fmt.Sprintf("%v, rollback failed too: %v", err, berr))
		return err
	}
	m.settle(ctx, s, &restored, "rolled back: "+err.Error())
	h.Logf("rolled back to %s", previous.GetRepo())
	return err
}

// Whether the occupant still serves and answers its health
func (m *Manager) serving(o occupant) (bool, string) {
	if o.f != nil {
		f, err := m.Formations.Get(o.f.GetId())
		if err != nil {
			return false, err.Error()
		}
		return f.GetState() == v1.FormationState_FORMATION_STATE_READY, "formation " + text.Enum(f.GetState())
	}
	in, err := m.Instances.Get(o.in.GetId())
	if err != nil {
		return false, err.Error()
	}
	return in.GetState() == v1.InstanceState_INSTANCE_STATE_READY && in.GetHealthy(), "instance " + text.Enum(in.GetState())
}

// Tracks occupants outside swaps, user stops emptying the slot and failures keeping its request
func (m *Manager) OnInstance(rec *v1.Instance) {
	if rec.GetSlotId() == "" {
		return
	}
	s, err := m.find(rec.GetSlotId())
	if err != nil || s.GetState() == v1.SlotState_SLOT_STATE_SWAPPING {
		return
	}
	if rec.GetId() != s.GetInstanceId() {
		// A formation keeps its slot and a stray instance never takes one with a live occupant
		if m.liveFormation(s) != nil {
			return
		}
		if s.GetInstanceId() != "" {
			if cur, err := m.Instances.Get(s.GetInstanceId()); err == nil && !instances.Terminal(cur.GetState()) {
				return
			}
		}
		if instances.Terminal(rec.GetState()) {
			return
		}
		s = m.update(s.GetId(), func(sl *v1.Slot) {
			sl.InstanceId, sl.DesiredRunning = rec.GetId(), true
			if rec.GetRequest() != nil {
				sl.Request = rec.GetRequest()
			}
		})
	}
	ctx := context.Background()
	switch rec.GetState() {
	case v1.InstanceState_INSTANCE_STATE_STOPPED:
		if rec.GetDesiredRunning() {
			m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_STARTING, "" })
			return
		}
		m.dropRoutes(ctx, s)
		m.update(s.GetId(), func(sl *v1.Slot) {
			sl.InstanceId, sl.State, sl.Error, sl.TaskId, sl.Request, sl.DesiredRunning = "", v1.SlotState_SLOT_STATE_EMPTY, "", "", nil, false
		})
	case v1.InstanceState_INSTANCE_STATE_FAILED:
		m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_FAILED, rec.GetError() })
	case v1.InstanceState_INSTANCE_STATE_READY:
		next := m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_READY, "" })
		if err := m.claimRoutes(ctx, next, occupant{in: rec}); err != nil {
			m.Log.Warn("slot routes not named", "slot", next.GetName(), "err", err)
		}
	default:
		m.update(s.GetId(), func(sl *v1.Slot) { sl.State = stateOf(rec) })
	}
}

// Wakes the occupant a slot or instance owner names, launching a desired slot's occupant again when its record is gone
func (m *Manager) Wake(ctx context.Context, owner pools.OwnerRef) error {
	switch owner.Kind {
	case pools.OwnerInstance:
		return m.Instances.Wake(ctx, owner)
	case pools.OwnerSlot:
	default:
		return fmt.Errorf("%w: %s owners never unload, so nothing wakes them", ErrSlot, owner.Kind)
	}
	s, err := m.find(owner.ID)
	if err != nil {
		return err
	}
	if s.GetInstanceId() != "" {
		if _, err := m.Instances.Get(s.GetInstanceId()); err == nil {
			return m.Instances.Wake(ctx, pools.OwnerRef{Kind: pools.OwnerInstance, ID: s.GetInstanceId()})
		}
	}
	switch {
	case s.GetState() == v1.SlotState_SLOT_STATE_SWAPPING:
		return fmt.Errorf("%w: slot %s is swapping", ErrSlot, s.GetName())
	case s.GetFormationId() != "":
		return fmt.Errorf("%w: slot %s serves formation %s, which never unloads", ErrSlot, s.GetName(), s.GetFormationId())
	case !s.GetDesiredRunning() || s.GetRequest() == nil:
		return fmt.Errorf("%w: slot %s wants nothing running", ErrSlot, s.GetName())
	case s.GetState() == v1.SlotState_SLOT_STATE_FAILED:
		return fmt.Errorf("%w: slot %s failed, relaunch it: %s", ErrSlot, s.GetName(), s.GetError())
	}
	_, _, _, err = m.Relaunch(ctx, s.GetId())
	return err
}

func modelOf(req *v1.RunRequest) string {
	if req == nil {
		return ""
	}
	return req.GetRepo() + ":" + req.GetGroup()
}

// Every slot as the node record advertises it
func (m *Manager) Refs() []*v1.SlotRef {
	var out []*v1.SlotRef
	for _, s := range m.List() {
		out = append(out, &v1.SlotRef{Id: s.GetId(), Name: s.GetName(), FormationId: s.GetFormationId(), DeviceIds: s.GetDeviceIds()})
	}
	return out
}
