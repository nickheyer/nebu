// Package slots reserves devices under stable names and swaps occupants.
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
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
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

// Manages slots and swaps. Run and swap set the saved request. Evict and stop
// clear it. Failures retain it for relaunch after restart.
type Manager struct {
	DB           *db.DB
	Instances    *instances.Manager
	Routes       *gateway.Table
	Tasks        *tasks.Manager
	Host         *host.Prober
	Events       *events.Bus
	DrainTimeout time.Duration
	Log          *slog.Logger

	mu    sync.Mutex
	slots map[string]*v1.Slot
	// Slots with active operations.
	busy map[string]bool
}

// Claims a slot unless another operation or swap holds it.
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

// Loads every slot from the store
func (m *Manager) Load(ctx context.Context) error {
	list, err := m.DB.ListSlots(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.slots = map[string]*v1.Slot{}
	for _, s := range list {
		m.slots[s.GetId()] = s
	}
	m.mu.Unlock()
	m.renumber()
	return nil
}

// Recovers live occupants and relaunches saved requests in position order.
// Slots that failed before restart stay failed until manually relaunched.
func (m *Manager) Recover(ctx context.Context) error {
	var relaunch []*v1.Slot
	for _, s := range m.List() {
		live := m.liveInstance(s)
		switch {
		case live != nil:
			s = m.update(s.GetId(), func(sl *v1.Slot) {
				sl.InstanceId = live.GetId()
				if live.GetState() == v1.InstanceState_INSTANCE_STATE_READY {
					sl.State = v1.SlotState_SLOT_STATE_READY
				} else {
					sl.State = v1.SlotState_SLOT_STATE_STARTING
				}
				if live.GetRequest() != nil {
					sl.Request = live.GetRequest()
				}
			})
			if live.GetState() == v1.InstanceState_INSTANCE_STATE_READY {
				m.route(s, live)
			} else {
				m.pending(s, modelOf(s.GetRequest()))
			}
		case s.GetRequest() != nil && s.GetState() == v1.SlotState_SLOT_STATE_FAILED:
			s = m.update(s.GetId(), func(sl *v1.Slot) { sl.InstanceId = "" })
			m.pending(s, modelOf(s.GetRequest()))
		case s.GetRequest() != nil && s.GetState() != v1.SlotState_SLOT_STATE_EMPTY:
			s = m.update(s.GetId(), func(sl *v1.Slot) {
				sl.InstanceId, sl.State, sl.Error = "", v1.SlotState_SLOT_STATE_STARTING, ""
			})
			m.pending(s, modelOf(s.GetRequest()))
			relaunch = append(relaunch, s)
		default:
			// Clear stale requests from slots saved before eviction cleared them.
			s = m.update(s.GetId(), func(sl *v1.Slot) {
				sl.InstanceId, sl.State, sl.Error, sl.TaskId, sl.Request = "", v1.SlotState_SLOT_STATE_EMPTY, "", "", nil
			})
			m.pending(s, "")
		}
	}
	// Drop routes of slots that no longer exist and names slots no longer have.
	m.Routes.Prune(func(r *v1.Route) bool {
		if r.GetSlotId() == "" {
			return true
		}
		s, err := m.find(r.GetSlotId())
		return err == nil && slices.Contains(names(s), r.GetName())
	})
	if len(relaunch) > 0 {
		go m.relaunchAll(ctx, relaunch)
	}
	return nil
}

// Relaunches slots serially so each plan accounts for earlier launches.
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

// Assigns positions from 1 in list order and returns changed slots.
// The caller must hold the lock.
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

// Stores and publishes changed slot positions.
func (m *Manager) persist(moved []*v1.Slot) {
	for _, s := range moved {
		if err := m.DB.PutSlot(context.Background(), s); err != nil {
			m.Log.Warn("slot record write failed", "id", s.GetId(), "err", err)
		}
		m.Events.Publish(v1.EventKind_EVENT_KIND_SLOT, v1.EventAction_EVENT_ACTION_UPDATED, s.GetId(), s)
	}
}

// Renumbers slots in their current sort order.
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

// Moves a slot and shifts neighboring positions.
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

// Applies fn under the lock, writes, and publishes the slot
func (m *Manager) update(id string, fn func(*v1.Slot)) *v1.Slot {
	m.mu.Lock()
	s, ok := m.slots[id]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	fn(s)
	s.UpdatedAt = timestamppb.Now()
	snapshot := proto.Clone(s).(*v1.Slot)
	m.mu.Unlock()
	if err := m.DB.PutSlot(context.Background(), snapshot); err != nil {
		m.Log.Warn("slot record write failed", "id", id, "err", err)
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_SLOT, v1.EventAction_EVENT_ACTION_UPDATED, id, snapshot)
	return snapshot
}

// Resolves a slot into planning constraints, implementing the instance manager's slots
func (m *Manager) Reservation(ctx context.Context, id string) (*instances.Reservation, error) {
	s, err := m.find(id)
	if err != nil {
		return nil, err
	}
	return &instances.Reservation{
		SlotID:      s.GetId(),
		Name:        s.GetName(),
		DeviceIDs:   s.GetDeviceIds(),
		MemoryBytes: s.GetMemoryBytes(),
		Placement:   s.GetPlacement(),
		RuntimeID:   s.GetRuntimeId(),
		Params:      s.GetParams(),
		InstanceID:  s.GetInstanceId(),
	}, nil
}

// Requires a nonempty name unused by other slots or routes.
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
	if r, taken := m.Routes.Lookup(name); taken && (self == "" || r.GetSlotId() != self) {
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

// Trims and checks a slot's alias list against its own name
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
		if p := a.GetPolicy(); p.GetMaxInFlight()+p.GetBurst()+p.GetRequestTimeoutMs()+p.GetUpstreamTimeoutMs() > 0 || p.GetRequestsPerSecond() > 0 {
			clean.Policy = proto.Clone(p).(*v1.Policy)
		}
		if a.GetProfile().GetSystemMessages() != v1.SystemMessages_SYSTEM_MESSAGES_UNSPECIFIED {
			clean.Profile = proto.Clone(a.GetProfile()).(*v1.Profile)
		}
		out = append(out, clean)
	}
	return out, nil
}

// Every public name of the slot: its own, then its aliases in order
func names(s *v1.Slot) []string {
	out := []string{s.GetName()}
	for _, a := range s.GetAliases() {
		out = append(out, a.GetName())
	}
	return out
}

// Limits and shaping for one of the slot's names. Alias fields override the slot's.
func settingsFor(s *v1.Slot, name string) (*v1.Policy, *v1.Profile) {
	for _, a := range s.GetAliases() {
		if a.GetName() != name {
			continue
		}
		policy, profile := s.GetPolicy(), s.GetProfile()
		if a.GetPolicy() != nil {
			policy = gateway.Effective(a.GetPolicy(), s.GetPolicy())
		}
		if a.GetProfile().GetSystemMessages() != v1.SystemMessages_SYSTEM_MESSAGES_UNSPECIFIED {
			profile = a.GetProfile()
		}
		return policy, profile
	}
	return s.GetPolicy(), s.GetProfile()
}

// Slot device pins.
func devicesFor(placement v1.Placement, ids []string) []string {
	if placement == v1.Placement_PLACEMENT_HOST {
		return nil
	}
	return ids
}

// Creates a slot, checking that its devices exist
func (m *Manager) Create(ctx context.Context, req *v1.CreateSlotRequest) (*v1.Slot, error) {
	name := strings.TrimSpace(req.GetName())
	if err := m.checkName(name, ""); err != nil {
		return nil, err
	}
	aliases, err := m.checkAliases(req.GetAliases(), "", name)
	if err != nil {
		return nil, err
	}
	devices := devicesFor(req.GetPlacement(), req.GetDeviceIds())
	if err := m.checkDevices(ctx, devices); err != nil {
		return nil, err
	}
	s := &v1.Slot{
		Id:          db.NewID(),
		Name:        name,
		Aliases:     aliases,
		Placement:   req.GetPlacement(),
		DeviceIds:   devices,
		MemoryBytes: req.GetMemoryBytes(),
		RuntimeId:   req.GetRuntimeId(),
		Params:      req.GetParams(),
		Policy:      req.GetPolicy(),
		Profile:     req.GetProfile(),
		State:       v1.SlotState_SLOT_STATE_EMPTY,
		CreatedAt:   timestamppb.Now(),
		UpdatedAt:   timestamppb.Now(),
	}
	m.mu.Lock()
	if m.slots == nil {
		m.slots = map[string]*v1.Slot{}
	}
	s.Position = uint32(len(m.slots) + 1)
	m.slots[s.GetId()] = s
	m.mu.Unlock()
	if err := m.DB.PutSlot(ctx, s); err != nil {
		m.mu.Lock()
		delete(m.slots, s.GetId())
		m.mu.Unlock()
		return nil, err
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_SLOT, v1.EventAction_EVENT_ACTION_CREATED, s.GetId(), s)
	if req.GetPosition() > 0 {
		m.place(s.GetId(), req.GetPosition())
	}
	m.pending(m.mustFind(s.GetId()), "")
	return m.find(s.GetId())
}

func (m *Manager) checkDevices(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	profile, err := m.Host.Profile(ctx, false)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, d := range profile.GetDevices() {
		known[d.GetId()] = true
	}
	for _, id := range ids {
		if !known[id] {
			return fmt.Errorf("%w: device %q was not probed, see nebu host", ErrSlot, id)
		}
	}
	return nil
}

// Updates name, aliases, and route limits immediately. Other settings apply on the next run.
func (m *Manager) Update(ctx context.Context, req *v1.UpdateSlotRequest) (*v1.Slot, error) {
	s, err := m.find(req.GetId())
	if err != nil {
		return nil, err
	}
	devices := devicesFor(req.GetPlacement(), req.GetDeviceIds())
	if err := m.checkDevices(ctx, devices); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		name = s.GetName()
	}
	aliases, err := m.checkAliases(req.GetAliases(), s.GetId(), name)
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
	// Names dropped from the alias list stop answering before the rename, so a
	// slot can take over one of its own former aliases as its name.
	m.dropNames(s, append([]string{s.GetName()}, aliasNames(aliases)...))
	if name != s.GetName() {
		if _, err := m.Routes.Rename(s.GetName(), name); err != nil && !errors.Is(err, gateway.ErrNoRoute) {
			return nil, fmt.Errorf("%w: %v", ErrSlot, err)
		}
	}
	next := m.update(s.GetId(), func(sl *v1.Slot) {
		sl.Name = name
		sl.Aliases = aliases
		sl.Placement = req.GetPlacement()
		sl.DeviceIds = devices
		sl.MemoryBytes = req.GetMemoryBytes()
		sl.RuntimeId = req.GetRuntimeId()
		sl.Params = req.GetParams()
		sl.Policy = req.GetPolicy()
		sl.Profile = req.GetProfile()
		if sl.Request != nil {
			sl.Request.Name = name
		}
	})
	if req.GetPosition() > 0 && req.GetPosition() != next.GetPosition() {
		m.place(next.GetId(), req.GetPosition())
		next = m.mustFind(next.GetId())
	}
	m.apply(next)
	return next, nil
}

func aliasNames(list []*v1.SlotAlias) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.GetName())
	}
	return out
}

// Removes the slot's routes except the kept names.
func (m *Manager) dropNames(s *v1.Slot, keep []string) []*v1.Route {
	return m.Routes.Prune(func(r *v1.Route) bool {
		return r.GetSlotId() != s.GetId() || slices.Contains(keep, r.GetName())
	})
}

// Points every name of the slot at its occupant
func (m *Manager) apply(s *v1.Slot) {
	live := m.liveInstance(s)
	switch {
	case s.GetState() == v1.SlotState_SLOT_STATE_SWAPPING, live != nil && live.GetState() == v1.InstanceState_INSTANCE_STATE_DRAINING:
		for _, name := range names(s) {
			if _, ok := m.Routes.Lookup(name); !ok {
				m.pendingName(s, name, modelOf(s.GetRequest()))
			}
		}
	case live != nil && live.GetState() == v1.InstanceState_INSTANCE_STATE_READY:
		m.route(s, live)
	default:
		m.pending(s, modelOf(s.GetRequest()))
	}
}

// Adds an alias to a slot, or updates its limits and shaping, and returns the alias route.
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
		for i, a := range sl.Aliases {
			if a.GetName() == clean.GetName() {
				sl.Aliases[i] = clean
				return
			}
		}
		sl.Aliases = append(sl.Aliases, clean)
	})
	m.apply(next)
	r, ok := m.Routes.Lookup(clean.GetName())
	if !ok {
		return nil, nil, fmt.Errorf("%w: alias %q was saved but has no route", ErrSlot, clean.GetName())
	}
	return next, r, nil
}

// Removes an alias from its slot and returns the route it answered as.
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
	next := m.update(s.GetId(), func(sl *v1.Slot) {
		sl.Aliases = slices.DeleteFunc(sl.Aliases, func(a *v1.SlotAlias) bool { return a.GetName() == name })
	})
	gone := m.dropNames(next, names(next))
	for _, r := range gone {
		if r.GetName() == name {
			return next, r, nil
		}
	}
	return next, &v1.Route{Name: name, SlotId: next.GetId()}, nil
}

// Deletes a slot, stopping its occupant when forced
func (m *Manager) Delete(ctx context.Context, id string, force bool) (*v1.Slot, error) {
	s, err := m.find(id)
	if err != nil {
		return nil, err
	}
	if err := m.claim(s.GetId()); err != nil {
		return nil, err
	}
	defer m.release(s.GetId())
	if live := m.liveInstance(s); live != nil {
		if !force {
			return nil, fmt.Errorf("%w: slot %s serves %s, evict it or pass --force", ErrSlot, s.GetName(), live.GetName())
		}
		if _, err := m.Instances.Stop(ctx, live.GetId()); err != nil {
			return nil, err
		}
	}
	if _, err := m.DB.DeleteSlot(ctx, s.GetId()); err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.slots, s.GetId())
	m.mu.Unlock()
	m.dropNames(s, nil)
	m.Events.Publish(v1.EventKind_EVENT_KIND_SLOT, v1.EventAction_EVENT_ACTION_DELETED, s.GetId(), s)
	m.renumber()
	return s, nil
}

// Stops the occupant and clears the saved request.
func (m *Manager) Evict(ctx context.Context, id string) (*v1.Slot, error) {
	s, err := m.find(id)
	if err != nil {
		return nil, err
	}
	if err := m.claim(s.GetId()); err != nil {
		return nil, err
	}
	defer m.release(s.GetId())
	for _, in := range m.Instances.InSlot(s.GetId()) {
		if err := m.retire(ctx, in.GetId()); err != nil {
			return nil, err
		}
	}
	next := m.update(s.GetId(), func(sl *v1.Slot) {
		sl.InstanceId, sl.State, sl.Error, sl.TaskId, sl.Request = "", v1.SlotState_SLOT_STATE_EMPTY, "", "", nil
	})
	m.pending(next, "")
	return next, nil
}

// Relaunches the slot's saved request.
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
	run := proto.Clone(s.GetRequest()).(*v1.RunRequest)
	run.SlotId, run.Name = s.GetId(), s.GetName()
	in, task, err := m.Instances.Run(ctx, run)
	if err != nil {
		next := m.update(s.GetId(), func(sl *v1.Slot) {
			sl.InstanceId, sl.State, sl.Error = "", v1.SlotState_SLOT_STATE_FAILED, err.Error()
		})
		m.pending(next, modelOf(next.GetRequest()))
		return nil, nil, nil, err
	}
	next := m.update(s.GetId(), func(sl *v1.Slot) {
		sl.InstanceId, sl.State, sl.Error, sl.TaskId, sl.Request = in.GetId(), v1.SlotState_SLOT_STATE_STARTING, "", task.GetId(), in.GetRequest()
	})
	return next, in, task, nil
}

// Drains an instance, then stops it
func (m *Manager) retire(ctx context.Context, id string) error {
	if _, err := m.Instances.Drain(ctx, id, m.DrainTimeout); err != nil {
		return err
	}
	_, err := m.Instances.Stop(ctx, id)
	return err
}

// Swaps the slot's model while retaining its route name.
func (m *Manager) Swap(ctx context.Context, req *v1.SwapRequest) (*v1.Slot, *v1.Instance, *v1.Task, error) {
	s, err := m.find(req.GetSlotId())
	if err != nil {
		return nil, nil, nil, err
	}
	run := proto.Clone(req.GetRun()).(*v1.RunRequest)
	if run == nil || run.GetRepo() == "" {
		return nil, nil, nil, fmt.Errorf("%w: swap needs a model to run", ErrSlot)
	}
	run.SlotId = s.GetId()
	run.Name = s.GetName()
	// Hold the claim until starting or swapping to prevent concurrent swaps.
	if err := m.claim(s.GetId()); err != nil {
		return nil, nil, nil, err
	}
	defer m.release(s.GetId())
	old := m.liveInstance(s)
	plan, err := m.Instances.Plan(ctx, run)
	if err != nil {
		return nil, nil, nil, err
	}
	drainFirst := req.GetDrainFirst() || old == nil || plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS
	if old == nil {
		in, task, err := m.Instances.Run(ctx, run)
		if err != nil {
			return nil, nil, nil, err
		}
		// Save the prepared request with its runtime and merged params.
		s = m.update(s.GetId(), func(sl *v1.Slot) {
			sl.InstanceId, sl.State, sl.Error, sl.TaskId, sl.Request = in.GetId(), v1.SlotState_SLOT_STATE_STARTING, "", task.GetId(), in.GetRequest()
		})
		return s, in, task, nil
	}
	m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_SWAPPING, "" })
	mode := "blue-green"
	if drainFirst {
		mode = "drain first"
	}
	title := fmt.Sprintf("swap %s to %s %s", s.GetName(), run.GetRepo(), run.GetGroup())
	task := m.Tasks.Start(kindSwap, title, map[string]string{"slot": s.GetId(), "name": s.GetName()}, func(ctx context.Context, h *tasks.Handle) error {
		h.Logf("swap mode %s, plan %s", mode, text.Enum(plan.GetVerdict()))
		if drainFirst {
			return m.swapDrainFirst(ctx, h, s, old, run)
		}
		return m.swapBlueGreen(ctx, h, s, old, run)
	})
	m.update(s.GetId(), func(sl *v1.Slot) { sl.TaskId = task.GetId() })
	return m.mustFind(s.GetId()), old, task, nil
}

// Launches the slot's model and returns the ready instance.
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

// Starts the replacement, switches the route, then drains the old instance.
func (m *Manager) swapBlueGreen(ctx context.Context, h *tasks.Handle, s *v1.Slot, old *v1.Instance, run *v1.RunRequest) error {
	h.Progress(0, 3, "starting "+run.GetRepo())
	fresh, err := m.launch(ctx, h, run, true)
	if err != nil {
		if fresh != nil {
			m.Instances.Stop(context.Background(), fresh.GetId())
		}
		m.settle(s, old, "", fmt.Errorf("new instance failed, %s still serving: %w", old.GetName(), err))
		return err
	}
	h.Progress(1, 3, "switching route")
	m.update(s.GetId(), func(sl *v1.Slot) { sl.InstanceId, sl.Request = fresh.GetId(), fresh.GetRequest() })
	m.route(m.mustFind(s.GetId()), fresh)
	h.Logf("route %s now serves %s", s.GetName(), fresh.GetId())
	h.Progress(2, 3, "draining "+old.GetId())
	m.retire(ctx, old.GetId())
	m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_READY, "" })
	h.Progress(3, 3, "ready")
	return nil
}

// Drains the old instance before launching, restoring it if launch fails.
func (m *Manager) swapDrainFirst(ctx context.Context, h *tasks.Handle, s *v1.Slot, old *v1.Instance, run *v1.RunRequest) error {
	h.Progress(0, 3, "draining "+old.GetId())
	m.pending(m.mustFind(s.GetId()), modelOf(run))
	if err := m.retire(ctx, old.GetId()); err != nil {
		m.settle(s, nil, "", err)
		return err
	}
	h.Logf("stopped %s", old.GetId())
	h.Progress(1, 3, "starting "+run.GetRepo())
	fresh, err := m.launch(ctx, h, run, false)
	if fresh != nil {
		m.update(s.GetId(), func(sl *v1.Slot) { sl.InstanceId = fresh.GetId() })
	}
	if err == nil {
		h.Progress(2, 3, "switching route")
		m.update(s.GetId(), func(sl *v1.Slot) { sl.Request = fresh.GetRequest() })
		m.route(m.mustFind(s.GetId()), fresh)
		m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_READY, "" })
		h.Progress(3, 3, "ready")
		return nil
	}
	h.Logf("new instance failed: %v", err)
	previous := s.GetRequest()
	if previous == nil {
		previous = old.GetRequest()
	}
	if previous == nil {
		m.settle(s, nil, "", err)
		return err
	}
	h.Progress(2, 3, "rolling back to "+previous.GetRepo())
	previous = proto.Clone(previous).(*v1.RunRequest)
	previous.SlotId = s.GetId()
	restored, berr := m.launch(ctx, h, previous, false)
	if restored != nil {
		m.update(s.GetId(), func(sl *v1.Slot) { sl.InstanceId, sl.Request = restored.GetId(), restored.GetRequest() })
	}
	if berr != nil {
		m.settle(s, nil, "", fmt.Errorf("%v, rollback failed too: %v", err, berr))
		return err
	}
	m.route(m.mustFind(s.GetId()), restored)
	m.settle(s, restored, "rolled back", err)
	h.Logf("rolled back to %s", previous.GetRepo())
	return err
}

// Updates the slot to the surviving instance and retains the request for relaunch.
func (m *Manager) settle(s *v1.Slot, serving *v1.Instance, note string, err error) {
	next := m.update(s.GetId(), func(sl *v1.Slot) {
		if serving != nil {
			sl.InstanceId, sl.State = serving.GetId(), v1.SlotState_SLOT_STATE_READY
		} else {
			sl.InstanceId, sl.State = "", v1.SlotState_SLOT_STATE_FAILED
		}
		sl.Error = strings.TrimSpace(note + " " + err.Error())
	})
	if serving == nil {
		m.pending(next, modelOf(next.GetRequest()))
	}
}

// Points the slot's name and aliases at an instance
func (m *Manager) route(s *v1.Slot, in *v1.Instance) {
	api := m.Instances.Runtimes.API(in.GetRuntimeId())
	for _, name := range names(s) {
		policy, profile := settingsFor(s, name)
		m.Routes.Serve(name, in, api, s.GetId(), policy, profile)
	}
}

// Keeps the slot's name and aliases pending without an instance.
func (m *Manager) pending(s *v1.Slot, model string) {
	for _, name := range names(s) {
		m.pendingName(s, name, model)
	}
}

func (m *Manager) pendingName(s *v1.Slot, name, model string) {
	policy, profile := settingsFor(s, name)
	m.Routes.Pending(name, s.GetId(), model, policy, profile)
}

// Tracks occupants outside swaps. User stops empty the slot. Failures retain
// the request in failed state. Shutdown leaves it starting for restart recovery.
func (m *Manager) OnInstance(rec *v1.Instance) {
	if rec.GetSlotId() == "" {
		return
	}
	s, err := m.find(rec.GetSlotId())
	if err != nil {
		return
	}
	if s.GetState() == v1.SlotState_SLOT_STATE_SWAPPING {
		return
	}
	if rec.GetId() != s.GetInstanceId() {
		if s.GetInstanceId() != "" {
			if cur, err := m.Instances.Get(s.GetInstanceId()); err == nil && !instances.Terminal(cur.GetState()) {
				return
			}
		}
		if instances.Terminal(rec.GetState()) {
			return
		}
		m.update(s.GetId(), func(sl *v1.Slot) {
			sl.InstanceId = rec.GetId()
			if rec.GetRequest() != nil {
				sl.Request = rec.GetRequest()
			}
		})
	}
	switch rec.GetState() {
	case v1.InstanceState_INSTANCE_STATE_STARTING:
		m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_STARTING, "" })
	case v1.InstanceState_INSTANCE_STATE_READY:
		m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_READY, "" })
		m.route(s, rec)
	case v1.InstanceState_INSTANCE_STATE_DRAINING:
		m.update(s.GetId(), func(sl *v1.Slot) { sl.State = v1.SlotState_SLOT_STATE_DRAINING })
	case v1.InstanceState_INSTANCE_STATE_STOPPED, v1.InstanceState_INSTANCE_STATE_FAILED:
		next := m.update(s.GetId(), func(sl *v1.Slot) {
			sl.InstanceId = ""
			switch {
			case rec.GetState() == v1.InstanceState_INSTANCE_STATE_FAILED:
				sl.State, sl.Error = v1.SlotState_SLOT_STATE_FAILED, rec.GetError()
			case rec.GetDesiredRunning():
				sl.State = v1.SlotState_SLOT_STATE_STARTING
			default:
				sl.State, sl.Error, sl.TaskId, sl.Request = v1.SlotState_SLOT_STATE_EMPTY, "", "", nil
			}
		})
		m.pending(next, modelOf(next.GetRequest()))
	}
}

func modelOf(req *v1.RunRequest) string {
	if req == nil {
		return ""
	}
	return req.GetRepo() + ":" + req.GetGroup()
}
