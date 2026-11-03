package formations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/tasks"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"google.golang.org/protobuf/proto"
)

// The delay before a replica seat relaunches, doubled after each relaunch up to the cap
var (
	seatBackoffMin = time.Second
	seatBackoffMax = time.Minute
	// Served whole this long, the next seat relaunch waits the first delay again
	seatBackoffReset = 5 * time.Minute
)

// Relaunches in a row a replica seat makes before it stays failed and the others serve on
const maxSeatRelaunches = 5

// One seat relaunch under way, cancelled by a stop and done once it returned
type reseat struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// The next seat relaunch delay of a replicas formation and since when it has served with every seat
type seatBackoff struct {
	next       time.Duration
	wholeSince time.Time
}

// Sends a seat that left ready to its own relaunch in a replicas formation, to the whole formation's degrade otherwise
func (m *Manager) seatLeft(f *v1.Formation, s *v1.Seat, why string) {
	if f.GetShape() == v1.Shape_SHAPE_REPLICAS {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.degradeSeat(f.GetId(), seatKey(s), why)
		}()
		return
	}
	if f.GetState() == v1.FormationState_FORMATION_STATE_READY {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.degrade(f.GetId(), why)
		}()
	}
}

// Records that a formation serves with every seat, the moment its backoff starts counting toward a reset
func (m *Manager) servedWhole(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.backoffs[id]
	if b == nil {
		b = &seatBackoff{next: seatBackoffMin}
		m.backoffs[id] = b
	}
	b.wholeSince = time.Now()
}

// Cancels every seat relaunch of a formation and waits for each, so no seat starts after a stop
func (m *Manager) cancelReseats(id string) {
	m.mu.Lock()
	running := m.reseats[id]
	delete(m.reseats, id)
	m.mu.Unlock()
	for _, r := range running {
		r.cancel()
	}
	for _, r := range running {
		<-r.done
	}
}

// The seats of a formation relaunching now, by seat key
func (m *Manager) relaunching(id string) map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]bool{}
	for key := range m.reseats[id] {
		out[key] = true
	}
	return out
}

// Takes one failed seat of a replicas formation out of its pool while the others serve, then relaunches it alone
func (m *Manager) degradeSeat(id, key, why string) {
	m.mu.Lock()
	f, ok := m.list[id]
	if !ok || f.GetConductor() != m.self() || !serving(f.GetState()) || m.stopping[id] || m.busy[id] || m.reseats[id][key] != nil {
		m.mu.Unlock()
		return
	}
	var seat *v1.Seat
	for _, s := range f.GetSeats() {
		if seatKey(s) == key {
			seat = proto.Clone(s).(*v1.Seat)
		}
	}
	if seat == nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(m.base)
	if m.reseats[id] == nil {
		m.reseats[id] = map[string]*reseat{}
	}
	entry := &reseat{cancel: cancel, done: make(chan struct{})}
	m.reseats[id][key] = entry
	b := m.backoffs[id]
	if b == nil {
		b = &seatBackoff{next: seatBackoffMin}
		m.backoffs[id] = b
	}
	if !b.wholeSince.IsZero() && time.Since(b.wholeSince) >= seatBackoffReset {
		b.next = seatBackoffMin
	}
	b.wholeSince = time.Time{}
	m.mu.Unlock()
	defer m.endReseat(id, key, entry)
	detail := fmt.Sprintf("%s%d on %s: %s", seat.GetRole(), seat.GetRank(), seat.GetNodeName(), why)
	m.Log.Warn("replica seat left, the others keep serving", "formation", f.GetName(), "seat", detail)
	m.update(id, func(r *v1.Formation) {
		r.State, r.Error = v1.FormationState_FORMATION_STATE_DEGRADED, detail
		r.Conditions = instances.SetCondition(r.Conditions, CondDegraded, true, ReasonSeatFailed, detail, r.GetGeneration())
	})
	if seat.GetInstanceId() != "" {
		m.Registrar.SetReady(ctx, seat.GetInstanceId(), false)
	}
	m.republish(ctx, id)
	if seat.GetInstanceId() != "" {
		m.drainSeat(ctx, f.GetName(), seat)
		sctx, scancel := context.WithTimeout(m.base, stopTimeout)
		if err := m.stopSeat(sctx, seat); err != nil {
			m.Log.Warn("failed replica seat did not stop", "formation", f.GetName(), "seat", detail, "err", err)
		}
		scancel()
	}
	m.relaunchSeat(ctx, id, key, entry)
}

// Waits for the requests this gateway has in flight on one seat, up to the drain timeout
func (m *Manager) drainSeat(ctx context.Context, formation string, seat *v1.Seat) {
	limit := m.DrainTimeout
	if limit <= 0 {
		limit = defaultDrainTimeout
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(drainPoll)
	defer tick.Stop()
	for m.InFlight(seat.GetInstanceId()) > 0 {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			m.Log.Warn("requests still in flight when the degraded seat stops", "formation", formation, "seat", seatKey(seat), "in_flight", m.InFlight(seat.GetInstanceId()))
			return
		case <-tick.C:
		}
	}
}

// Drops a seat's relaunch entry when it is still the one listed and marks the relaunch done
func (m *Manager) endReseat(id, key string, entry *reseat) {
	m.mu.Lock()
	if m.reseats[id][key] == entry {
		delete(m.reseats[id], key)
		if len(m.reseats[id]) == 0 {
			delete(m.reseats, id)
		}
	}
	m.mu.Unlock()
	entry.cancel()
	select {
	case <-entry.done:
	default:
		close(entry.done)
	}
}

// Publishes the formation's ready seats without the ones relaunching, readiness following whether any serves
func (m *Manager) republish(ctx context.Context, id string) {
	if err := m.publishSeats(ctx, id, m.relaunching(id)); err != nil {
		m.Log.Warn("formation endpoints not republished, the failed seat stays out of traffic by its ready mark", "formation", id, "err", err)
	}
}

// Names a serving formation's route again so its status carries the seats serving it now
func (m *Manager) reroute(ctx context.Context, id string) error {
	f, err := m.Get(id)
	if err != nil {
		return err
	}
	if f.GetSlotId() != "" || !serving(f.GetState()) {
		return nil
	}
	if _, err := m.Registrar.NameRoute(ctx, ownerOf(id), f.GetName(), routeSpec(f)); err != nil {
		return fmt.Errorf("route %s: %w", f.GetName(), err)
	}
	return nil
}

// Takes the next seat relaunch delay of a formation and doubles the one after it up to the cap
func (m *Manager) nextSeatDelay(id string) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.backoffs[id]
	d := b.next
	b.next = min(b.next*2, seatBackoffMax)
	return d
}

// The inputs one seat of a replicas formation launches with, from the record and its slot
func (m *Manager) seatInputs(f *v1.Formation) (*planned, error) {
	if f.GetShape() != v1.Shape_SHAPE_REPLICAS {
		return nil, fmt.Errorf("%w: a seat relaunches alone only in a replicas formation, %s is %s", ErrFormation, f.GetName(), shapeWord(f.GetShape()))
	}
	rt, err := m.Runtimes.Get(f.GetRuntimeId())
	if err != nil {
		return nil, err
	}
	params := f.GetRequest().GetParams()
	var slotArgs string
	if f.GetSlotId() != "" {
		if m.SlotView == nil {
			return nil, fmt.Errorf("%w: slot %s: no slots are wired to the conductor", ErrFormation, f.GetSlotId())
		}
		slot, _, err := m.SlotView.Get(f.GetSlotId())
		if err != nil {
			return nil, fmt.Errorf("%w: slot %s: %v", ErrFormation, f.GetSlotId(), err)
		}
		params, slotArgs = runtimes.Merge(slot.GetParams(), params), slot.GetArgs()
	}
	return &planned{rt: rt, name: f.GetName(), params: params, args: runtimes.JoinArgs(slotArgs, f.GetRequest().GetArgs())}, nil
}

// Relaunches one replica seat with backoff until it serves, the formation stops, or it fails too often
func (m *Manager) relaunchSeat(ctx context.Context, id, key string, entry *reseat) {
	up := false
	for failures := 1; ; failures++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.nextSeatDelay(id)):
		}
		f, err := m.Get(id)
		if err != nil || !f.GetDesiredRunning() || !serving(f.GetState()) || m.isStopping(id) {
			return
		}
		var seat *v1.Seat
		for _, s := range f.GetSeats() {
			if seatKey(s) == key {
				seat = s
			}
		}
		if seat == nil {
			return
		}
		if !up {
			err = m.reseatOnce(ctx, f, seat)
			if ctx.Err() != nil {
				return
			}
			up = err == nil
		}
		if up {
			if err = m.seatBack(ctx, id, key, entry); err == nil {
				return
			}
		}
		m.Log.Warn("replica seat relaunch failed", "formation", f.GetName(), "seat", key, "running", up, "failures", failures, "err", err)
		if failures >= maxSeatRelaunches {
			detail := fmt.Sprintf("%s%d on %s stays out after %d tries in a row, the last: %v", seat.GetRole(), seat.GetRank(), seat.GetNodeName(), failures, err)
			m.update(id, func(r *v1.Formation) {
				r.Error = detail
				r.Conditions = instances.SetCondition(r.Conditions, CondDegraded, true, ReasonSeatFailed, detail, r.GetGeneration())
			})
			return
		}
	}
}

// Launches one seat again in a task of its own and waits for it to be ready
func (m *Manager) reseatOnce(ctx context.Context, f *v1.Formation, seat *v1.Seat) error {
	p, err := m.seatInputs(f)
	if err != nil {
		return err
	}
	labels := map[string]string{"formation": f.GetId(), "name": f.GetName(), "seat": seatKey(seat)}
	title := fmt.Sprintf("relaunch %s%d of %s on %s", seat.GetRole(), seat.GetRank(), f.GetName(), seat.GetNodeName())
	task := m.Tasks.Start(kindFormation, title, labels, func(tctx context.Context, h *tasks.Handle) error {
		in, err := m.startSeat(tctx, h, f, p, seat, f.GetRendezvous(), &launched{byKey: map[string]*v1.Instance{}})
		if err != nil {
			return err
		}
		_, err = m.awaitSeat(tctx, h, f, p, seat, in)
		return err
	})
	final, err := m.Tasks.Wait(ctx, task.GetId())
	if err != nil {
		if _, cerr := m.Tasks.Cancel(task.GetId()); cerr == nil {
			m.Tasks.Wait(context.WithoutCancel(ctx), task.GetId())
		}
		return err
	}
	if final.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		return errors.New(final.GetError())
	}
	return nil
}

// Publishes a relaunched seat with the others, the formation ready again once no other seat is out
func (m *Manager) seatBack(ctx context.Context, id, key string, entry *reseat) error {
	skip := m.relaunching(id)
	delete(skip, key)
	if err := m.publishSeats(ctx, id, skip); err != nil {
		return err
	}
	if err := m.reroute(ctx, id); err != nil {
		return err
	}
	m.endReseat(id, key, entry)
	if len(m.relaunching(id)) > 0 {
		return nil
	}
	m.update(id, func(r *v1.Formation) {
		if r.State == v1.FormationState_FORMATION_STATE_DEGRADED {
			markReady(r)
		}
	})
	m.servedWhole(id)
	m.Mesh.Bump()
	return nil
}
