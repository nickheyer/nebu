package slots

import (
	"context"
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
	List(runningOnly bool) []*v1.Formation
	Node(id string) (*v1.Node, error)
	Self() string
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

// Whether a run in the slot is a formation, naming a span or shape or pinning devices elsewhere
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

// A slot's formation request carrying every setting it launches with, the run's over the slot's, its devices the default span
func (m *Manager) meshRequest(s *v1.Slot, run *v1.RunRequest) *v1.RunRequest {
	out := proto.Clone(run).(*v1.RunRequest)
	out.SlotId, out.Name = s.GetId(), s.GetName()
	if !out.GetExact() {
		if out.RuntimeId == "" {
			out.RuntimeId = s.GetRuntimeId()
		}
		out.Params = runtimes.Merge(s.GetParams(), run.GetParams())
		out.Args = runtimes.RunArgs(s.GetArgs(), run.GetArgs())
		out.Exact = true
	}
	if len(out.Span) == 0 && len(s.GetDeviceIds()) > 0 {
		for _, id := range s.GetDeviceIds() {
			node, device := m.splitDevice(id)
			out.Span = append(out.Span, node+"/"+device)
		}
	}
	return out
}

// Settles the slot on its formation's settings and keeps the formation's model and route as its request
func settleFormation(sl *v1.Slot, run *v1.RunRequest) {
	if settle(sl, run, true) {
		bump(sl)
	}
	sl.Request = held(run)
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

// Launches the slot's formation, records it on the slot, and names the slot's routes at it
func (m *Manager) launchFormation(ctx context.Context, s *v1.Slot, run *v1.RunRequest) (*v1.Formation, *v1.Task, error) {
	f, task, err := m.Formations.Run(ctx, m.meshRequest(s, run))
	if err != nil {
		m.update(s.GetId(), func(sl *v1.Slot) {
			sl.InstanceId, sl.FormationId, sl.State, sl.Error = "", "", v1.SlotState_SLOT_STATE_FAILED, err.Error()
		})
		return nil, nil, err
	}
	next := m.update(s.GetId(), func(sl *v1.Slot) {
		sl.InstanceId, sl.FormationId, sl.State, sl.Error, sl.TaskId, sl.DesiredRunning = "", f.GetId(), v1.SlotState_SLOT_STATE_STARTING, "", task.GetId(), true
		settleFormation(sl, f.GetRequest())
	})
	if err := m.claimRoutes(ctx, next, occupant{f: f}); err != nil {
		m.Log.Warn("slot routes not named", "slot", next.GetName(), "err", err)
	}
	return f, task, nil
}

// Launches a formation under the run's name and waits for it to be ready
func (m *Manager) formationReady(ctx context.Context, h *tasks.Handle, s *v1.Slot, run *v1.RunRequest, swap bool) (*v1.Formation, error) {
	if swap {
		ctx = instances.WithSwap(ctx)
	}
	req := m.meshRequest(s, run)
	req.Name = run.GetName()
	f, task, err := m.Formations.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	h.Logf("starting %s as formation %s, %s across %d seats", run.GetRepo(), f.GetId(), strings.ToLower(strings.TrimPrefix(f.GetShape().String(), "SHAPE_")), len(f.GetSeats()))
	if err := m.Tasks.WaitOK(ctx, task.GetId()); err != nil {
		return f, err
	}
	return m.Formations.Get(f.GetId())
}

// Stops whatever a slot serves, its formation, its instances, or both, each through its drain
func (m *Manager) retireAll(ctx context.Context, s *v1.Slot) error {
	if f := m.liveFormation(s); f != nil {
		if _, err := m.Formations.Stop(ctx, f.GetId()); err != nil {
			return err
		}
	}
	for _, in := range m.Instances.InSlot(s.GetId()) {
		if _, err := m.Instances.Stop(ctx, in.GetId()); err != nil {
			return err
		}
	}
	return nil
}

// Shapes a run aimed at the conductor by its slot, refusing an occupied slot outside a swap
func (m *Manager) Claim(ctx context.Context, slotID string, run *v1.RunRequest) (*v1.RunRequest, error) {
	s, err := m.find(slotID)
	if err != nil {
		return nil, err
	}
	req := m.meshRequest(s, run)
	if instances.FromSwap(ctx) {
		// A swap names its replacement, a canary taking a name of its own beside the occupant
		if run.GetName() != "" {
			req.Name = run.GetName()
		}
		return req, nil
	}
	if f := m.liveFormation(s); f != nil {
		return nil, fmt.Errorf("%w: slot %s serves formation %s, swap instead", ErrSlot, s.GetName(), f.GetName())
	}
	if in := m.liveInstance(s); in != nil {
		return nil, fmt.Errorf("%w: slot %s serves %s, swap instead", ErrSlot, s.GetName(), in.GetName())
	}
	return req, nil
}

// Follows the slot's formation, its state becoming the slot's and its end emptying or failing the slot
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
		s = m.update(s.GetId(), func(sl *v1.Slot) {
			sl.FormationId, sl.InstanceId, sl.DesiredRunning = f.GetId(), "", true
			if f.GetRequest() != nil {
				settleFormation(sl, f.GetRequest())
			}
		})
	}
	ctx := context.Background()
	switch f.GetState() {
	case v1.FormationState_FORMATION_STATE_STARTING:
		m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_STARTING, "" })
	case v1.FormationState_FORMATION_STATE_READY:
		next := m.update(s.GetId(), func(sl *v1.Slot) { sl.State, sl.Error = v1.SlotState_SLOT_STATE_READY, "" })
		if err := m.claimRoutes(ctx, next, occupant{f: f}); err != nil {
			m.Log.Warn("slot routes not named", "slot", next.GetName(), "err", err)
		}
	case v1.FormationState_FORMATION_STATE_DEGRADED, v1.FormationState_FORMATION_STATE_STOPPING:
		m.update(s.GetId(), func(sl *v1.Slot) { sl.State = v1.SlotState_SLOT_STATE_DRAINING })
	case v1.FormationState_FORMATION_STATE_STOPPED, v1.FormationState_FORMATION_STATE_FAILED:
		if !f.GetDesiredRunning() && f.GetState() == v1.FormationState_FORMATION_STATE_STOPPED {
			m.dropRoutes(ctx, s)
		}
		m.update(s.GetId(), func(sl *v1.Slot) {
			sl.FormationId = ""
			switch {
			case f.GetState() == v1.FormationState_FORMATION_STATE_FAILED:
				sl.State, sl.Error = v1.SlotState_SLOT_STATE_FAILED, f.GetError()
			case f.GetDesiredRunning():
				sl.State = v1.SlotState_SLOT_STATE_STARTING
			default:
				sl.State, sl.Error, sl.TaskId, sl.Request, sl.DesiredRunning = v1.SlotState_SLOT_STATE_EMPTY, "", "", nil, false
			}
		})
	}
}
