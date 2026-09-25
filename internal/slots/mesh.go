package slots

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/tasks"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"google.golang.org/protobuf/proto"
)

// The conductor, for slots whose span reaches other nodes
type Formations interface {
	Run(ctx context.Context, run *v1.RunRequest) (*v1.Formation, *v1.Task, error)
	Plan(ctx context.Context, run *v1.RunRequest) (*v1.FormationPlan, *v1.Descriptor, error)
	Stop(ctx context.Context, id string) (*v1.Formation, error)
	Get(id string) (*v1.Formation, error)
	Node(id string) (*v1.Node, error)
	Self() string
}

// One of a slot's names with the settings its route takes
type RouteName struct {
	Name    string
	Policy  *v1.Policy
	Profile *v1.Profile
}

// Splits a slot device id into its node and device, this node for a bare id
func (m *Manager) splitDevice(id string) (node, device string) {
	if i := strings.Index(id, "/"); i > 0 {
		return id[:i], id[i+1:]
	}
	if m.Formations != nil {
		return m.Formations.Self(), id
	}
	return "", id
}

// Whether a run in the slot is a formation: it names a span or a shape, or the slot's devices
// sit on another node
func (m *Manager) meshRun(s *v1.Slot, run *v1.RunRequest) bool {
	if m.Formations == nil {
		return false
	}
	if len(run.GetSpan()) > 0 || run.GetShape() != v1.Shape_SHAPE_UNSPECIFIED {
		return true
	}
	for _, id := range s.GetDeviceIds() {
		if node, _ := m.splitDevice(id); node != "" && node != m.Formations.Self() {
			return true
		}
	}
	return false
}

// The request a slot's formation runs with: the slot's name, runtime, and parameters under the
// request's, and the slot's devices as the span when the request names none
func (m *Manager) meshRequest(s *v1.Slot, run *v1.RunRequest) *v1.RunRequest {
	out := proto.Clone(run).(*v1.RunRequest)
	out.SlotId, out.Name = s.GetId(), s.GetName()
	if out.RuntimeId == "" {
		out.RuntimeId = s.GetRuntimeId()
	}
	out.Params = runtimes.Merge(s.GetParams(), run.GetParams())
	if len(out.Span) == 0 && len(s.GetDeviceIds()) > 0 {
		for _, id := range s.GetDeviceIds() {
			node, device := m.splitDevice(id)
			out.Span = append(out.Span, node+"/"+device)
		}
	}
	return out
}

// The slot's live formation, nil when none
func (m *Manager) liveFormation(s *v1.Slot) *v1.Formation {
	if m.Formations == nil || s.GetFormationId() == "" {
		return nil
	}
	f, err := m.Formations.Get(s.GetFormationId())
	if err != nil {
		return nil
	}
	switch f.GetState() {
	case v1.FormationState_FORMATION_STATE_STOPPED, v1.FormationState_FORMATION_STATE_FAILED:
		return nil
	}
	return f
}

// Launches the slot's formation and records it on the slot
func (m *Manager) launchFormation(ctx context.Context, s *v1.Slot, run *v1.RunRequest) (*v1.Formation, *v1.Task, error) {
	f, task, err := m.Formations.Run(ctx, m.meshRequest(s, run))
	if err != nil {
		next := m.update(s.GetId(), func(sl *v1.Slot) {
			sl.InstanceId, sl.FormationId, sl.State, sl.Error = "", "", v1.SlotState_SLOT_STATE_FAILED, err.Error()
		})
		m.pending(next, modelOf(next.GetRequest()))
		return nil, nil, err
	}
	m.update(s.GetId(), func(sl *v1.Slot) {
		sl.InstanceId, sl.FormationId, sl.State, sl.Error, sl.TaskId, sl.Request = "", f.GetId(), v1.SlotState_SLOT_STATE_STARTING, "", task.GetId(), f.GetRequest()
	})
	return f, task, nil
}

// Launches a formation and waits for it to be ready
func (m *Manager) formationReady(ctx context.Context, h *tasks.Handle, s *v1.Slot, run *v1.RunRequest, swap bool) (*v1.Formation, error) {
	if swap {
		ctx = instances.WithSwap(ctx)
	}
	f, task, err := m.Formations.Run(ctx, m.meshRequest(s, run))
	if err != nil {
		return nil, err
	}
	h.Logf("starting %s as formation %s, %s across %d seats", run.GetRepo(), f.GetId(), strings.ToLower(strings.TrimPrefix(f.GetShape().String(), "SHAPE_")), len(f.GetSeats()))
	if err := m.Tasks.WaitOK(ctx, task.GetId()); err != nil {
		return f, err
	}
	return m.Formations.Get(f.GetId())
}

// Stops whatever a slot serves: its formation, its instances, or both
func (m *Manager) retireAll(ctx context.Context, s *v1.Slot) error {
	if f := m.liveFormation(s); f != nil {
		m.Routes.DrainFormation(f.GetId())
		if _, err := m.Formations.Stop(ctx, f.GetId()); err != nil {
			return err
		}
	}
	for _, in := range m.Instances.InSlot(s.GetId()) {
		if err := m.retire(ctx, in.GetId()); err != nil {
			return err
		}
	}
	return nil
}

// Swaps a slot to a formation: the new one beside the old when the mesh fits both, else the old
// one drained first
func (m *Manager) swapFormation(ctx context.Context, req *v1.SwapRequest, s *v1.Slot, run *v1.RunRequest) (*v1.Slot, *v1.Instance, *v1.Task, error) {
	oldFormation, oldInstance := m.liveFormation(s), m.liveInstance(s)
	plan, _, err := m.Formations.Plan(ctx, m.meshRequest(s, run))
	if err != nil {
		return nil, nil, nil, err
	}
	if oldFormation == nil && oldInstance == nil {
		_, task, err := m.launchFormation(ctx, s, run)
		if err != nil {
			return nil, nil, nil, err
		}
		return m.mustFind(s.GetId()), nil, task, nil
	}
	drainFirst := req.GetDrainFirst() || plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS
	m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_SWAPPING, "" })
	mode := "blue-green"
	if drainFirst {
		mode = "drain first"
	}
	title := fmt.Sprintf("swap %s to %s %s across the mesh", s.GetName(), run.GetRepo(), run.GetGroup())
	task := m.Tasks.Start(kindSwap, title, map[string]string{"slot": s.GetId(), "name": s.GetName()}, func(ctx context.Context, h *tasks.Handle) error {
		h.Logf("swap mode %s, plan %s: %s", mode, strings.ToLower(strings.TrimPrefix(plan.GetVerdict().String(), "FIT_VERDICT_")), plan.GetDetail())
		if drainFirst {
			return m.swapFormationDrainFirst(ctx, h, s, run)
		}
		return m.swapFormationBlueGreen(ctx, h, s, oldFormation, oldInstance, run)
	})
	m.update(s.GetId(), func(sl *v1.Slot) { sl.TaskId = task.GetId() })
	return m.mustFind(s.GetId()), oldInstance, task, nil
}

// Launches the new formation beside the old, switches the route, then stops the old
func (m *Manager) swapFormationBlueGreen(ctx context.Context, h *tasks.Handle, s *v1.Slot, oldFormation *v1.Formation, oldInstance *v1.Instance, run *v1.RunRequest) error {
	h.Progress(0, 3, "starting "+run.GetRepo())
	fresh, err := m.formationReady(ctx, h, s, run, true)
	if err != nil {
		if fresh != nil {
			if _, serr := m.Formations.Stop(context.Background(), fresh.GetId()); serr != nil {
				err = fmt.Errorf("%w; the failed formation %s did not stop either: %v", err, fresh.GetId(), serr)
			}
		}
		m.settleFormation(s, oldFormation, oldInstance, fmt.Errorf("new formation failed, the old one still serves: %w", err))
		return err
	}
	h.Progress(1, 3, "switching route")
	m.update(s.GetId(), func(sl *v1.Slot) { sl.FormationId, sl.InstanceId, sl.Request = fresh.GetId(), "", fresh.GetRequest() })
	h.Logf("route %s now serves formation %s", s.GetName(), fresh.GetId())
	h.Progress(2, 3, "stopping the old occupant")
	var stops []error
	if oldFormation != nil {
		m.Routes.DrainFormation(oldFormation.GetId())
		if _, err := m.Formations.Stop(ctx, oldFormation.GetId()); err != nil {
			stops = append(stops, fmt.Errorf("the old formation %s did not stop: %w", oldFormation.GetId(), err))
		}
	}
	if oldInstance != nil {
		if err := m.retire(ctx, oldInstance.GetId()); err != nil {
			stops = append(stops, fmt.Errorf("the old instance %s did not stop: %w", oldInstance.GetId(), err))
		}
	}
	if len(stops) > 0 {
		// The new formation serves; the old occupant still holds its memory, which the slot says.
		err := errors.Join(stops...)
		h.Logf("%v", err)
		m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_READY, err.Error() })
		return err
	}
	m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_READY, "" })
	h.Progress(3, 3, "ready")
	return nil
}

// Stops the old occupant, then launches the formation, bringing the previous occupant back when
// the launch fails, as a solo swap does
func (m *Manager) swapFormationDrainFirst(ctx context.Context, h *tasks.Handle, s *v1.Slot, run *v1.RunRequest) error {
	h.Progress(0, 3, "stopping the old occupant")
	m.pending(m.mustFind(s.GetId()), modelOf(run))
	if err := m.retireAll(ctx, s); err != nil {
		m.settleFormation(s, nil, nil, err)
		return err
	}
	h.Progress(1, 3, "starting "+run.GetRepo())
	fresh, err := m.formationReady(ctx, h, s, run, false)
	if fresh != nil {
		m.update(s.GetId(), func(sl *v1.Slot) { sl.FormationId, sl.InstanceId = fresh.GetId(), "" })
	}
	if err == nil {
		h.Progress(2, 3, "switching route")
		m.update(s.GetId(), func(sl *v1.Slot) {
			sl.Request, sl.State, sl.Error = fresh.GetRequest(), v1.SlotState_SLOT_STATE_READY, ""
		})
		h.Progress(3, 3, "ready")
		return nil
	}
	h.Logf("new formation failed: %v", err)
	previous := s.GetRequest()
	if previous == nil {
		m.settleFormation(s, nil, nil, err)
		return err
	}
	h.Progress(2, 3, "rolling back to "+previous.GetRepo())
	previous = proto.Clone(previous).(*v1.RunRequest)
	previous.SlotId = s.GetId()
	if m.meshRun(s, previous) {
		restored, berr := m.formationReady(ctx, h, s, previous, false)
		if berr != nil {
			m.settleFormation(s, nil, nil, fmt.Errorf("%v, rollback failed too: %v", err, berr))
			return err
		}
		m.update(s.GetId(), func(sl *v1.Slot) { sl.Request = restored.GetRequest() })
		m.settleFormation(s, restored, nil, fmt.Errorf("rolled back: %w", err))
		h.Logf("rolled back to formation %s", restored.GetId())
		return err
	}
	restored, berr := m.launch(ctx, h, previous, false)
	if restored != nil {
		m.update(s.GetId(), func(sl *v1.Slot) {
			sl.FormationId, sl.InstanceId, sl.Request = "", restored.GetId(), restored.GetRequest()
		})
	}
	if berr != nil {
		m.settleFormation(s, nil, nil, fmt.Errorf("%v, rollback failed too: %v", err, berr))
		return err
	}
	m.route(m.mustFind(s.GetId()), restored)
	m.settleFormation(s, nil, restored, fmt.Errorf("rolled back: %w", err))
	h.Logf("rolled back to %s", previous.GetRepo())
	return err
}

// Updates the slot to whatever survives a formation swap
func (m *Manager) settleFormation(s *v1.Slot, f *v1.Formation, in *v1.Instance, err error) {
	next := m.update(s.GetId(), func(sl *v1.Slot) {
		switch {
		case f != nil:
			sl.FormationId, sl.InstanceId, sl.State = f.GetId(), "", v1.SlotState_SLOT_STATE_READY
		case in != nil:
			sl.FormationId, sl.InstanceId, sl.State = "", in.GetId(), v1.SlotState_SLOT_STATE_READY
		default:
			sl.FormationId, sl.InstanceId, sl.State = "", "", v1.SlotState_SLOT_STATE_FAILED
		}
		sl.Error = err.Error()
	})
	if f == nil && in == nil {
		m.pending(next, modelOf(next.GetRequest()))
	}
}

// Claims a slot for a formation launched straight at the conductor: the slot's name, runtime,
// parameters, and devices shape the request, and an occupied slot refuses unless a swap is under way
func (m *Manager) Claim(ctx context.Context, slotID string, run *v1.RunRequest) (*v1.RunRequest, error) {
	s, err := m.find(slotID)
	if err != nil {
		return nil, err
	}
	if !instances.FromSwap(ctx) {
		if f := m.liveFormation(s); f != nil {
			return nil, fmt.Errorf("%w: slot %s serves formation %s, swap instead", ErrSlot, s.GetName(), f.GetName())
		}
		if in := m.liveInstance(s); in != nil {
			return nil, fmt.Errorf("%w: slot %s serves %s, swap instead", ErrSlot, s.GetName(), in.GetName())
		}
	}
	return m.meshRequest(s, run), nil
}

// The slot's names with the settings each route takes, for the conductor
func (m *Manager) Names(slotID string) []RouteName {
	s, err := m.find(slotID)
	if err != nil {
		return nil
	}
	var out []RouteName
	for _, name := range names(s) {
		policy, profile := settingsFor(s, name)
		out = append(out, RouteName{Name: name, Policy: policy, Profile: profile})
	}
	return out
}

// Follows the slot's formation: its state becomes the slot's, and a finished one empties the slot
// or leaves it failed with the reason
func (m *Manager) OnFormation(f *v1.Formation) {
	if f.GetSlotId() == "" {
		return
	}
	s, err := m.find(f.GetSlotId())
	if err != nil || s.GetState() == v1.SlotState_SLOT_STATE_SWAPPING {
		return
	}
	if f.GetId() != s.GetFormationId() {
		if live := m.liveFormation(s); live != nil && live.GetId() != f.GetId() {
			return
		}
		switch f.GetState() {
		case v1.FormationState_FORMATION_STATE_STOPPED, v1.FormationState_FORMATION_STATE_FAILED:
			return
		}
		m.update(s.GetId(), func(sl *v1.Slot) {
			sl.FormationId, sl.InstanceId = f.GetId(), ""
			if f.GetRequest() != nil {
				sl.Request = f.GetRequest()
			}
		})
	}
	switch f.GetState() {
	case v1.FormationState_FORMATION_STATE_STARTING:
		m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_STARTING, "" })
	case v1.FormationState_FORMATION_STATE_READY:
		m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_READY, "" })
	case v1.FormationState_FORMATION_STATE_DEGRADED, v1.FormationState_FORMATION_STATE_STOPPING:
		m.update(s.GetId(), func(sl *v1.Slot) { sl.State = v1.SlotState_SLOT_STATE_DRAINING })
	case v1.FormationState_FORMATION_STATE_STOPPED, v1.FormationState_FORMATION_STATE_FAILED:
		next := m.update(s.GetId(), func(sl *v1.Slot) {
			sl.FormationId = ""
			switch {
			case f.GetState() == v1.FormationState_FORMATION_STATE_FAILED:
				sl.State, sl.Error = v1.SlotState_SLOT_STATE_FAILED, f.GetError()
			case f.GetDesiredRunning():
				sl.State = v1.SlotState_SLOT_STATE_STARTING
			default:
				sl.State, sl.Error, sl.TaskId, sl.Request = v1.SlotState_SLOT_STATE_EMPTY, "", "", nil
			}
		})
		m.pending(next, modelOf(next.GetRequest()))
	}
}
