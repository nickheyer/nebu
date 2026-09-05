// Package slots reserves devices under stable names and swaps occupants.
package slots

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/eval"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const kindSwap = "swap"

var (
	// Returned when a slot id or name is not known
	ErrUnknownSlot = errors.New("unknown slot")
	// Returned when a slot is busy or a request malformed
	ErrSlot = errors.New("invalid slot request")
	// Returned when a slot cannot go because a watch or want swaps into it
	ErrSlotInUse = errors.New("slot in use")
)

// Owns every slot and drives swaps
type Manager struct {
	DB           *db.DB
	Instances    *instances.Manager
	Routes       *gateway.Table
	Tasks        *tasks.Manager
	Host         *host.Prober
	Events       *events.Bus
	DrainTimeout time.Duration
	Log          *slog.Logger
	// Names what swaps into a slot, watches and wants, dropping the swap when clear is set
	Referrers func(id string, clear bool) []string

	mu    sync.Mutex
	slots map[string]*v1.Slot
	// Slots a swap, evict, or delete is working on right now
	busy map[string]bool
}

// Reserves a slot for one operation, refusing while another holds it or a swap runs
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
	return nil
}

// Reconciles slots with what survived a restart, keeping names routable
func (m *Manager) Recover(ctx context.Context) error {
	for _, s := range m.List() {
		live := m.liveInstance(s)
		s = m.update(s.GetId(), func(sl *v1.Slot) {
			switch {
			case live == nil:
				sl.InstanceId = ""
				if sl.State != v1.SlotState_SLOT_STATE_FAILED {
					sl.State = v1.SlotState_SLOT_STATE_EMPTY
				}
			case live.GetState() == v1.InstanceState_INSTANCE_STATE_READY:
				sl.InstanceId, sl.State = live.GetId(), v1.SlotState_SLOT_STATE_READY
			default:
				sl.InstanceId, sl.State = live.GetId(), v1.SlotState_SLOT_STATE_STARTING
			}
		})
		if live != nil && live.GetState() == v1.InstanceState_INSTANCE_STATE_READY {
			m.route(s, live)
		} else {
			m.pending(s, modelOf(s.GetRequest()))
		}
	}
	return nil
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

// Lists slots by name
func (m *Manager) List() []*v1.Slot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*v1.Slot, 0, len(m.slots))
	for _, s := range m.slots {
		out = append(out, proto.Clone(s).(*v1.Slot))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out
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
		RuntimeID:   s.GetRuntimeId(),
		Params:      s.GetParams(),
		InstanceID:  s.GetInstanceId(),
	}, nil
}

// Creates a slot, checking that its devices exist
func (m *Manager) Create(ctx context.Context, req *v1.CreateSlotRequest) (*v1.Slot, error) {
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, fmt.Errorf("%w: name required", ErrSlot)
	}
	if _, err := m.find(name); err == nil {
		return nil, fmt.Errorf("%w: slot %q exists", ErrSlot, name)
	}
	if err := m.checkDevices(ctx, req.GetDeviceIds()); err != nil {
		return nil, err
	}
	if _, taken := m.Routes.Lookup(name); taken {
		return nil, fmt.Errorf("%w: %q is already a route, pick another name", ErrSlot, name)
	}
	s := &v1.Slot{
		Id:          db.NewID(),
		Name:        name,
		Description: req.GetDescription(),
		DeviceIds:   req.GetDeviceIds(),
		MemoryBytes: req.GetMemoryBytes(),
		RuntimeId:   req.GetRuntimeId(),
		Params:      req.GetParams(),
		Policy:      req.GetPolicy(),
		State:       v1.SlotState_SLOT_STATE_EMPTY,
		CreatedAt:   timestamppb.Now(),
		UpdatedAt:   timestamppb.Now(),
	}
	if err := m.DB.PutSlot(ctx, s); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.slots == nil {
		m.slots = map[string]*v1.Slot{}
	}
	m.slots[s.GetId()] = s
	m.mu.Unlock()
	m.pending(s, "")
	m.Events.Publish(v1.EventKind_EVENT_KIND_SLOT, v1.EventAction_EVENT_ACTION_CREATED, s.GetId(), s)
	return proto.Clone(s).(*v1.Slot), nil
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

// Changes settings, the limits reaching the route at once and the rest applying on the next run
func (m *Manager) Update(ctx context.Context, req *v1.UpdateSlotRequest) (*v1.Slot, error) {
	s, err := m.find(req.GetId())
	if err != nil {
		return nil, err
	}
	if err := m.checkDevices(ctx, req.GetDeviceIds()); err != nil {
		return nil, err
	}
	next := m.update(s.GetId(), func(sl *v1.Slot) {
		sl.Description = req.GetDescription()
		sl.DeviceIds = req.GetDeviceIds()
		sl.MemoryBytes = req.GetMemoryBytes()
		sl.RuntimeId = req.GetRuntimeId()
		sl.Params = req.GetParams()
		sl.Policy = req.GetPolicy()
	})
	// A swap in flight writes the route itself when it settles
	if next.GetState() != v1.SlotState_SLOT_STATE_SWAPPING {
		if live := m.liveInstance(next); live != nil && live.GetState() == v1.InstanceState_INSTANCE_STATE_READY {
			m.route(next, live)
		} else if live == nil || live.GetState() == v1.InstanceState_INSTANCE_STATE_STARTING {
			m.pending(next, modelOf(next.GetRequest()))
		}
	}
	return next, nil
}

// Names the slots whose last request starts from a profile, the one a rollback
// replays, dropping the reference when clear is set
func (m *Manager) ProfileReferrers(refers func(ref, runtimeID string) bool, clear bool) []string {
	var out []string
	for _, s := range m.List() {
		req := s.GetRequest()
		if !refers(req.GetProfileId(), req.GetRuntimeId()) {
			continue
		}
		out = append(out, "slot "+s.GetName())
		if clear {
			m.update(s.GetId(), func(sl *v1.Slot) { sl.Request.ProfileId = "" })
		}
	}
	return out
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
	if m.Referrers != nil {
		if used := m.Referrers(s.GetId(), false); len(used) > 0 && !force {
			return nil, fmt.Errorf("%w: slot %s is swapped into by %s, point them elsewhere or pass --force to drop the swap", ErrSlotInUse, s.GetName(), strings.Join(used, ", "))
		} else if len(used) > 0 {
			m.Referrers(s.GetId(), true)
		}
	}
	if _, err := m.DB.DeleteSlot(ctx, s.GetId()); err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.slots, s.GetId())
	m.mu.Unlock()
	m.Routes.Delete(s.GetName())
	m.Events.Publish(v1.EventKind_EVENT_KIND_SLOT, v1.EventAction_EVENT_ACTION_DELETED, s.GetId(), s)
	return s, nil
}

// Stops the occupant, keeping the slot and its name
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
	return m.update(s.GetId(), func(sl *v1.Slot) {
		sl.InstanceId, sl.State, sl.Error = "", v1.SlotState_SLOT_STATE_EMPTY, ""
	}), nil
}

// Drains an instance, then stops it
func (m *Manager) retire(ctx context.Context, id string) error {
	if _, err := m.Instances.Drain(ctx, id, m.DrainTimeout); err != nil {
		return err
	}
	_, err := m.Instances.Stop(ctx, id)
	return err
}

// Replaces what a slot serves, keeping its name answering throughout
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
	if run.GetName() == "" {
		run.Name = s.GetName()
	}
	// The claim holds until the slot is either starting or marked swapping, so two swaps cannot interleave
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
		// The instance holds the request as prepared, its profile by id and its runtime named
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
		h.Logf("swap mode %s, plan %s", mode, eval.EnumShort(plan.GetVerdict()))
		if drainFirst {
			return m.swapDrainFirst(ctx, h, s, old, run)
		}
		return m.swapBlueGreen(ctx, h, s, old, run)
	})
	m.update(s.GetId(), func(sl *v1.Slot) { sl.TaskId = task.GetId() })
	return m.mustFind(s.GetId()), old, task, nil
}

// Starts a run for the slot and waits for it to be ready, the fresh record on success
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

// Starts the new instance beside the old, flips, then drains
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

// Drains the old, starts the new, rolls back on failure
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
		m.update(s.GetId(), func(sl *v1.Slot) { sl.InstanceId = restored.GetId() })
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

// Settles the slot after a swap onto whichever instance survives
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
		m.pending(next, "")
	}
}

// Points the slot name at an instance
func (m *Manager) route(s *v1.Slot, in *v1.Instance) {
	m.Routes.Serve(s.GetName(), in, m.Instances.Runtimes.API(in.GetRuntimeId()), s.GetId(), s.GetPolicy())
}

// Keeps the slot name answering with nothing behind it
func (m *Manager) pending(s *v1.Slot, model string) {
	m.Routes.Pending(s.GetName(), s.GetId(), model, s.GetPolicy())
}

// Tracks occupants started or lost outside a swap, implementing the instance manager's slots
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
		m.update(s.GetId(), func(sl *v1.Slot) {
			sl.InstanceId = ""
			if rec.GetState() == v1.InstanceState_INSTANCE_STATE_FAILED {
				sl.State, sl.Error = v1.SlotState_SLOT_STATE_FAILED, rec.GetError()
			} else if rec.GetDesiredRunning() {
				sl.State = v1.SlotState_SLOT_STATE_STARTING
			} else {
				sl.State, sl.Error = v1.SlotState_SLOT_STATE_EMPTY, ""
			}
		})
		m.pending(s, modelOf(s.GetRequest()))
	}
}

func modelOf(req *v1.RunRequest) string {
	if req == nil {
		return ""
	}
	return req.GetRepo() + ":" + req.GetGroup()
}
