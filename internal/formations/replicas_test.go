package formations

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

// A conductor whose seats run on this node through a real instances manager over the fake runtime
type replicasNode struct {
	m        *Manager
	launcher *fake.Launcher
	reg      *fake.Registrar
	traffic  *fake.Traffic
	reads    *flightReads
}

// Counts the conductor's reads of each instance's requests in flight, answering from the traffic
type flightReads struct {
	traffic *fake.Traffic
	mu      sync.Mutex
	n       map[string]int
}

func (r *flightReads) inFlight(id string) int {
	r.mu.Lock()
	r.n[id]++
	r.mu.Unlock()
	return r.traffic.InFlight(id)
}

// Times the conductor read an instance's requests in flight so far
func (r *flightReads) count(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n[id]
}

func newReplicasNode(t *testing.T, ctx context.Context) *replicasNode {
	t.Helper()
	m := argsConductor(t, ctx)
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New()
	reg, traffic, launcher := fake.NewRegistrar(), fake.NewTraffic(), fake.New()
	work := tasks.New(ctx, log, m.DB, bus)
	prober := host.New([]host.Probe{argsProbe{}}, nil, time.Minute)
	m.Instances = &instances.Manager{
		DB:         m.DB,
		Dir:        filepath.Join(dir, "instances"),
		Store:      m.Store,
		Runtimes:   m.Runtimes,
		Installs:   m.Mesh.Installs,
		Inspector:  m.Inspector,
		Launcher:   launcher,
		Tasks:      work,
		Host:       prober,
		Registrar:  reg,
		InFlight:   traffic.InFlight,
		Dispatched: traffic.Dispatched,
		Load:       traffic.Load,
		Listening:  launcher.Listening,
		Events:     bus,
		Log:        log,
	}
	m.Instances.Formations = m
	reads := &flightReads{traffic: traffic, n: map[string]int{}}
	m.Registrar, m.InFlight = reg, reads.inFlight
	m.Tasks, m.Events, m.Installs = work, bus, m.Mesh.Installs
	m.CacheDir = filepath.Join(dir, "cache")
	m.DrainTimeout = 10 * time.Second
	m.planner = func(ctx context.Context, run *v1.RunRequest) (*planned, error) {
		stored, err := m.Store.ReadManifest(run.GetSourceId(), run.GetRepo(), run.GetGroup())
		if err != nil {
			return nil, err
		}
		seats := []*v1.Seat{
			{NodeId: m.self(), NodeName: "here", Role: runtimes.RoleReplica, Rank: 0, Phase: 1},
			{NodeId: m.self(), NodeName: "here", Role: runtimes.RoleReplica, Rank: 1, Phase: 1},
		}
		plan := &v1.FormationPlan{Shape: v1.Shape_SHAPE_REPLICAS, Seats: seats, RuntimeId: fake.RuntimeID, Detail: "two replicas here", SchedulerConfig: "replicas"}
		return &planned{plan: plan, descriptor: stored.GetDescriptor_(), stored: stored, rt: fake.Runtime{}, name: run.GetName(), params: map[string]string{}}, nil
	}
	if err := m.Open(ctx); err != nil {
		t.Fatal(err)
	}
	m.Start()
	t.Cleanup(func() {
		m.Instances.Close()
		m.Close()
	})
	return &replicasNode{m: m, launcher: launcher, reg: reg, traffic: traffic, reads: reads}
}

// Runs a two replica formation and waits for it to be ready
func (n *replicasNode) run(t *testing.T, ctx context.Context, name string) *v1.Formation {
	t.Helper()
	f, task, err := n.m.Run(ctx, &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.m.Tasks.WaitOK(ctx, task.GetId()); err != nil {
		t.Fatalf("formation launch: %v", err)
	}
	ready, err := n.m.Get(f.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if ready.GetState() != v1.FormationState_FORMATION_STATE_READY {
		t.Fatalf("formation %s: %s", ready.GetState(), ready.GetError())
	}
	return ready
}

// Waits for a condition, failing the test when it never holds
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not happen", what)
}

// The ranks a formation's published endpoints carry, in order
func ranks(eps []pools.Published) []uint32 {
	var out []uint32
	for _, ep := range eps {
		out = append(out, ep.Rank)
	}
	slices.Sort(out)
	return out
}

// A crashed replica leaves the pool while the other serves, relaunches alone, and comes back with the formation ready
func TestReplicasDegradeOneSeat(t *testing.T) {
	seatBackoffMin = 50 * time.Millisecond
	t.Cleanup(func() { seatBackoffMin = time.Second })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newReplicasNode(t, ctx)
	f := n.run(t, ctx, "rep")
	owner := pools.OwnerRef{Kind: pools.OwnerFormation, ID: f.GetId()}
	eps := n.reg.Endpoints(owner)
	if got := ranks(eps); !slices.Equal(got, []uint32{0, 1}) {
		t.Fatalf("published ranks %v", got)
	}
	for _, ep := range eps {
		if ep.Labels[instances.LabelServed] != "rep" || ep.NodeID != n.m.self() || ep.Address.Port == 0 {
			t.Fatalf("endpoint %+v", ep)
		}
	}
	if member, ok := n.reg.Membership(owner); !ok || member.SchedulerConfig != "replicas" || member.Runtime != fake.RuntimeID {
		t.Fatalf("membership %+v", member)
	}
	rt, ok := n.reg.Named("rep")
	if !ok || rt.Owner != owner || rt.Spec.Shape != v1.Shape_SHAPE_REPLICAS || !slices.Equal(rt.Spec.Seats, pools.SeatIDs(f)) || len(rt.Spec.Seats) != 2 {
		t.Fatalf("route %+v %v, want shape replicas and seats %v", rt, ok, pools.SeatIDs(f))
	}
	if !instances.ConditionTrue(f.GetConditions(), CondReady) || f.GetObservedGeneration() != 1 {
		t.Fatalf("conditions %v observed %d", f.GetConditions(), f.GetObservedGeneration())
	}
	for _, s := range f.GetSeats() {
		if !instances.ConditionTrue(s.GetConditions(), instances.CondReady) || !instances.ConditionTrue(s.GetConditions(), instances.CondLaunched) {
			t.Fatalf("seat %s%d carries its instance's conditions: %v", s.GetRole(), s.GetRank(), s.GetConditions())
		}
	}

	if !n.launcher.Crash("rep/replica1") {
		t.Fatal("no replica1 process")
	}
	eventually(t, "the formation degrading with replica0 alone published", func() bool {
		cur, err := n.m.Get(f.GetId())
		return err == nil && cur.GetState() == v1.FormationState_FORMATION_STATE_DEGRADED && slices.Equal(ranks(n.reg.Endpoints(owner)), []uint32{0})
	})
	cur, _ := n.m.Get(f.GetId())
	if c := instances.ConditionOf(cur.GetConditions(), CondDegraded); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE || c.GetReason() != ReasonSeatFailed {
		t.Fatalf("degraded condition %v", c)
	}
	if n.launcher.Launches("rep/replica0") != 1 {
		t.Fatal("the serving replica was relaunched")
	}
	eventually(t, "replica1 relaunched and the formation ready again", func() bool {
		cur, err := n.m.Get(f.GetId())
		return err == nil && cur.GetState() == v1.FormationState_FORMATION_STATE_READY && slices.Equal(ranks(n.reg.Endpoints(owner)), []uint32{0, 1}) && n.launcher.Launches("rep/replica1") == 2
	})
	cur, _ = n.m.Get(f.GetId())
	if instances.ConditionTrue(cur.GetConditions(), CondDegraded) {
		t.Fatalf("still degraded %v", cur.GetConditions())
	}
	for _, s := range cur.GetSeats() {
		if !instances.ConditionTrue(s.GetConditions(), instances.CondReady) {
			t.Fatalf("seat %s%d after the relaunch: %v", s.GetRole(), s.GetRank(), s.GetConditions())
		}
	}
	if n.launcher.Launches("rep/replica0") != 1 {
		t.Fatal("the serving replica was relaunched")
	}
	if back, ok := n.reg.Named("rep"); !ok || !slices.Equal(back.Spec.Seats, pools.SeatIDs(cur)) || slices.Equal(back.Spec.Seats, rt.Spec.Seats) || back.Spec.Shape != v1.Shape_SHAPE_REPLICAS {
		t.Fatalf("route after the relaunch %+v, want the seats serving now %v and not %v", back, pools.SeatIDs(cur), rt.Spec.Seats)
	}
}

// A stop drains the formation and waits for requests in flight before any seat stops
func TestFormationStopDrainsFirst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newReplicasNode(t, ctx)
	f := n.run(t, ctx, "drained")
	owner := pools.OwnerRef{Kind: pools.OwnerFormation, ID: f.GetId()}
	busy := f.GetSeats()[0].GetInstanceId()
	n.traffic.SetInFlight(busy, 2)
	polled := n.reads.count(busy)
	done := make(chan *v1.Formation, 1)
	go func() {
		out, err := n.m.Stop(ctx, f.GetId())
		if err != nil {
			t.Error(err)
		}
		done <- out
	}()
	eventually(t, "the formation drained and polling its requests in flight", func() bool { return n.reg.Drains(owner) == 1 && n.reads.count(busy) >= polled+3 })
	if stopped := n.launcher.StopOrder(); len(stopped) > 0 {
		t.Fatalf("seats %v stopped with requests in flight", stopped)
	}
	n.traffic.SetInFlight(busy, 0)
	var out *v1.Formation
	select {
	case out = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("stop did not finish")
	}
	if out.GetState() != v1.FormationState_FORMATION_STATE_STOPPED {
		t.Fatalf("state %s", out.GetState())
	}
	if len(n.launcher.StopOrder()) != 2 {
		t.Fatalf("stopped %v", n.launcher.StopOrder())
	}
	if len(n.reg.Endpoints(owner)) != 0 {
		t.Fatal("endpoints still published")
	}
	if _, ok := n.reg.Route("drained"); ok {
		t.Fatal("route still named")
	}
	history := n.reg.History()
	drain, unpublish := slices.Index(history, "drain "+f.GetId()), slices.Index(history, "unpublish "+f.GetId())
	if drain < 0 || unpublish < drain {
		t.Fatalf("history %v", history)
	}
}

// A replica seat leaving with a request in flight keeps running until the request ends, then stops and relaunches
func TestDegradeSeatDrainsFirst(t *testing.T) {
	seatBackoffMin = 50 * time.Millisecond
	t.Cleanup(func() { seatBackoffMin = time.Second })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newReplicasNode(t, ctx)
	f := n.run(t, ctx, "held")
	owner := pools.OwnerRef{Kind: pools.OwnerFormation, ID: f.GetId()}
	var kept, leaving *v1.Seat
	for _, s := range f.GetSeats() {
		if s.GetRank() == 1 {
			leaving = s
		} else {
			kept = s
		}
	}
	busy := leaving.GetInstanceId()
	n.traffic.SetInFlight(busy, 1)
	polled := n.reads.count(busy)
	n.m.seatsMissing(&v1.Node{Id: n.m.self(), Name: "here", State: v1.NodeState_NODE_STATE_READY, Profile: &v1.HostProfile{}, Seats: []*v1.SeatRef{{FormationId: f.GetId(), InstanceId: kept.GetInstanceId()}}})
	eventually(t, "the formation degrading with replica0 alone published", func() bool {
		cur, err := n.m.Get(f.GetId())
		return err == nil && cur.GetState() == v1.FormationState_FORMATION_STATE_DEGRADED && slices.Equal(ranks(n.reg.Endpoints(owner)), []uint32{0}) && !n.reg.Ready(busy)
	})
	eventually(t, "the conductor polling the leaving seat's requests in flight", func() bool { return n.reads.count(busy) >= polled+3 })
	if in, err := n.m.Instances.Get(busy); err != nil || !in.GetDesiredRunning() || in.GetState() != v1.InstanceState_INSTANCE_STATE_READY {
		t.Fatalf("the leaving seat was stopped with a request in flight: %v %v", in, err)
	}
	if stopped := n.launcher.StopOrder(); len(stopped) > 0 {
		t.Fatalf("seats %v stopped with a request in flight", stopped)
	}
	n.traffic.SetInFlight(busy, 0)
	eventually(t, "the leaving seat stopped once its request ended", func() bool { return slices.Equal(n.launcher.StopOrder(), []string{"held/replica1"}) })
	eventually(t, "replica1 relaunched and the formation ready again", func() bool {
		cur, err := n.m.Get(f.GetId())
		return err == nil && cur.GetState() == v1.FormationState_FORMATION_STATE_READY && slices.Equal(ranks(n.reg.Endpoints(owner)), []uint32{0, 1}) && n.launcher.Launches("held/replica1") == 2
	})
	if n.launcher.Launches("held/replica0") != 1 || slices.Contains(n.launcher.StopOrder(), "held/replica0") {
		t.Fatalf("the serving replica was stopped or relaunched: stopped %v", n.launcher.StopOrder())
	}
}

// A lockstep formation whose rank dies drains, stops every seat, fails, and relaunches as itself until ready again
func TestFormationRelaunchesWhole(t *testing.T) {
	relaunchDelay = 50 * time.Millisecond
	t.Cleanup(func() { relaunchDelay = 10 * time.Second })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newReplicasNode(t, ctx)
	n.m.planner = func(ctx context.Context, run *v1.RunRequest) (*planned, error) {
		stored, err := n.m.Store.ReadManifest(run.GetSourceId(), run.GetRepo(), run.GetGroup())
		if err != nil {
			return nil, err
		}
		seats := []*v1.Seat{
			{NodeId: n.m.self(), NodeName: "here", Role: runtimes.RoleHead, Rank: 0, Phase: 1},
			{NodeId: n.m.self(), NodeName: "here", Role: runtimes.RoleRank, Rank: 1, Phase: 1},
		}
		plan := &v1.FormationPlan{Shape: v1.Shape_SHAPE_LOCKSTEP, Seats: seats, RuntimeId: fake.RuntimeID, Detail: "a head and a rank here"}
		return &planned{plan: plan, descriptor: stored.GetDescriptor_(), stored: stored, rt: fake.Runtime{}, name: run.GetName(), params: map[string]string{}}, nil
	}
	f := n.run(t, ctx, "whole")
	owner := pools.OwnerRef{Kind: pools.OwnerFormation, ID: f.GetId()}
	if f.GetShape() != v1.Shape_SHAPE_LOCKSTEP || f.GetRendezvous() == "" || len(n.reg.Endpoints(owner)) == 0 {
		t.Fatalf("formation %s rendezvous %q endpoints %v", f.GetShape(), f.GetRendezvous(), n.reg.Endpoints(owner))
	}
	if rt, ok := n.reg.Named("whole"); !ok || rt.Owner != owner {
		t.Fatalf("route %+v %v", rt, ok)
	}
	var head string
	for _, s := range f.GetSeats() {
		if s.GetRole() == runtimes.RoleHead {
			head = s.GetInstanceId()
		}
	}
	n.traffic.SetInFlight(head, 1)
	polled := n.reads.count(head)
	if !n.launcher.Crash("whole/rank1") {
		t.Fatal("no rank1 process")
	}
	eventually(t, "the degraded formation draining its head", func() bool { return n.reg.Drains(owner) == 1 && n.reads.count(head) >= polled+3 })
	cur, err := n.m.Get(f.GetId())
	if err != nil || cur.GetState() != v1.FormationState_FORMATION_STATE_DEGRADED {
		t.Fatalf("formation while draining %v %v", cur.GetState(), err)
	}
	if in, err := n.m.Instances.Get(head); err != nil || !in.GetDesiredRunning() || in.GetState() != v1.InstanceState_INSTANCE_STATE_READY {
		t.Fatalf("the head was stopped with a request in flight: %v %v", in, err)
	}
	n.traffic.SetInFlight(head, 0)
	var back *v1.Formation
	eventually(t, "the relaunched formation ready", func() bool {
		for _, g := range n.m.List(false) {
			if g.GetName() == "whole" && g.GetId() != f.GetId() && g.GetState() == v1.FormationState_FORMATION_STATE_READY {
				back = g
				return true
			}
		}
		return false
	})
	old, err := n.m.Get(f.GetId())
	if err != nil || old.GetState() != v1.FormationState_FORMATION_STATE_FAILED {
		t.Fatalf("the degraded formation %v %v", old.GetState(), err)
	}
	if c := instances.ConditionOf(old.GetConditions(), CondDegraded); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE || c.GetReason() != ReasonFormationDown {
		t.Fatalf("degraded condition %v", c)
	}
	if back.GetRelaunches() != 1 || back.GetRendezvous() != f.GetRendezvous() || back.GetShape() != v1.Shape_SHAPE_LOCKSTEP || !instances.ConditionTrue(back.GetConditions(), CondReady) || instances.ConditionTrue(back.GetConditions(), CondDegraded) {
		t.Fatalf("relaunched formation relaunches %d rendezvous %q shape %s conditions %v", back.GetRelaunches(), back.GetRendezvous(), back.GetShape(), back.GetConditions())
	}
	backOwner := pools.OwnerRef{Kind: pools.OwnerFormation, ID: back.GetId()}
	if rt, ok := n.reg.Named("whole"); !ok || rt.Owner != backOwner {
		t.Fatalf("route after the relaunch %+v %v, want owner %v", rt, ok, backOwner)
	}
	if len(n.reg.Endpoints(owner)) != 0 || len(n.reg.Endpoints(backOwner)) == 0 {
		t.Fatalf("endpoints of the failed %v and the relaunched %v", n.reg.Endpoints(owner), n.reg.Endpoints(backOwner))
	}
	for _, name := range []string{"whole/head0", "whole/rank1"} {
		if got := n.launcher.Launches(name); got != 2 {
			t.Fatalf("%d launches of %s", got, name)
		}
	}
	history := n.reg.History()
	drain, unpublish := slices.Index(history, "drain "+f.GetId()), slices.Index(history, "unpublish "+f.GetId())
	if drain < 0 || unpublish < drain {
		t.Fatalf("history %v", history)
	}
}
