package formations_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/formations"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/instances/fake"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A chain launches its stage first and its head after the stage is ready, records every seat's
// endpoint, transport, measurements, and guard admissions, counts what the head streamed through
// the guard, advances one progress step per seat with one pull row per seat, and stops the head
// first after the requests in flight finish
func TestChainLaunchRecordAndStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := mesh2(t, ctx)
	stored := a.storeModel("src", "org/model", "q4")
	f, task, err := a.formations.StartPlanned(ctx, runOf(map[string]string{fake.ParamTransport: "sockets"}), formations.Planned{Plan: chainPlan(a, b), Stored: stored, Rt: fake.Runtime{}, Name: modelKey})
	if err != nil {
		t.Fatal(err)
	}
	if f.GetCacheKey() != formations.CacheKeyOf(stored) || f.GetCacheKey() == "" {
		t.Fatalf("cache key %q", f.GetCacheKey())
	}
	f = waitFormation(t, a, f.GetId(), v1.FormationState_FORMATION_STATE_READY)
	stage, head := seatByRole(f, runtimes.RoleStage), seatByRole(f, runtimes.RoleHead)
	stageProc, headProc := b.launcher.Proc(modelKey+"/stage1"), a.launcher.Proc(modelKey+"/head0")
	if stageProc == nil || headProc == nil || !stageProc.LaunchedAt.Before(headProc.LaunchedAt) {
		t.Fatalf("the stage launches before the head: stage %v head %v", stageProc, headProc)
	}
	if stage.GetState() != v1.InstanceState_INSTANCE_STATE_READY || head.GetState() != v1.InstanceState_INSTANCE_STATE_READY {
		t.Fatalf("seats %v", f.GetSeats())
	}
	if !strings.HasPrefix(stage.GetEndpoint(), "tcp://127.0.0.1:") || stage.GetExposed() || stage.GetTransport() != "sockets" {
		t.Fatalf("stage seat %v", stage)
	}
	stageIn, err := b.instances.Get(stage.GetInstanceId())
	if err != nil {
		t.Fatal(err)
	}
	spec := stageIn.GetSeat()
	if len(spec.GetAdmit()) != 1 || spec.GetAdmit()[0] != "127.0.0.1" {
		t.Fatalf("the stage's guard admits the conductor and the head alone: %v", spec.GetAdmit())
	}
	if spec.GetPort() == spec.GetLocalPort() || spec.GetPort() == 0 {
		t.Fatalf("the stage sits behind a guard listener: port %d local %d", spec.GetPort(), spec.GetLocalPort())
	}
	if spec.GetInterface() == "" {
		t.Fatalf("the stage's seat block names the interface of the link to its head: %v", spec)
	}
	if !strings.HasSuffix(spec.GetCacheDir(), f.GetCacheKey()) {
		t.Fatalf("the stage caches under the model's key, not the formation's id: %s", spec.GetCacheDir())
	}
	if f.GetBytesMoved() != fake.StreamBytes {
		t.Fatalf("bytes moved %d, the head streamed %d through the guard", f.GetBytesMoved(), fake.StreamBytes)
	}
	if len(stage.GetMeasurements()) == 0 || len(head.GetMeasurements()) == 0 {
		t.Fatalf("every seat's measurements reach the record: stage %v head %v", stage.GetMeasurements(), head.GetMeasurements())
	}
	byNode := map[string]uint64{}
	for _, nm := range f.GetMeasurements() {
		for _, ms := range nm.GetMeasurements() {
			if ms.GetKey() == "weights" {
				byNode[nm.GetNodeId()] = ms.GetBytes()
			}
		}
	}
	if byNode[a.mesh.Self()] == 0 || byNode[b.mesh.Self()] == 0 {
		t.Fatalf("measurements summed by node: %v", f.GetMeasurements())
	}
	done, err := a.tasks.Wait(ctx, task.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED || done.GetProgress().GetDone() != 2 || done.GetProgress().GetTotal() != 2 {
		t.Fatalf("one progress step per seat: %v", done.GetProgress())
	}
	rows := done.GetProgress().GetRows()
	if len(rows) != 1 || !strings.HasPrefix(rows[0].GetKey(), "head0 on ") || rows[0].GetDone() != 1 || rows[0].GetTotal() != 1 {
		t.Fatalf("one pull row for the seat holding weights: %v", rows)
	}
	route, ok := a.routes.Lookup(modelKey)
	if !ok || route.GetFormationId() != f.GetId() || len(route.GetSeats()) != 2 || route.GetState() != v1.RouteState_ROUTE_STATE_READY {
		t.Fatalf("route %v", route)
	}
	eventually(t, "the stage's node advertises its seat", func() bool {
		rec, err := a.mesh.Node(b.mesh.Self())
		if err != nil {
			return false
		}
		for _, ref := range rec.GetSeats() {
			if ref.GetInstanceId() == stage.GetInstanceId() && ref.GetState() == v1.InstanceState_INSTANCE_STATE_READY {
				return true
			}
		}
		return false
	})
	eventually(t, "the other member copies the formation and forwards its route", func() bool {
		copyOf, err := b.formations.Get(f.GetId())
		if err != nil || copyOf.GetState() != v1.FormationState_FORMATION_STATE_READY {
			return false
		}
		r, ok := b.routes.Lookup(modelKey)
		return ok && r.GetForwarded() && r.GetNodeId() == a.mesh.Self()
	})
	// A request in flight holds the stop until it finishes.
	release := a.routes.HoldSeat(head.GetInstanceId())
	go func() {
		time.Sleep(200 * time.Millisecond)
		release()
	}()
	start := time.Now()
	stopped, err := a.formations.Stop(ctx, f.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatalf("the stop waited for the request in flight: %s", time.Since(start))
	}
	if stopped.GetState() != v1.FormationState_FORMATION_STATE_STOPPED {
		t.Fatalf("stopped %v", stopped)
	}
	if !headProc.StoppedAt.Before(stageProc.StoppedAt) {
		t.Fatalf("the head stops before the stage: head %s stage %s", headProc.StoppedAt, stageProc.StoppedAt)
	}
	if _, ok := a.routes.Lookup(modelKey); ok {
		t.Fatal("the route is gone after the stop")
	}
	if in, err := b.instances.Get(stage.GetInstanceId()); err != nil || in.GetState() != v1.InstanceState_INSTANCE_STATE_STOPPED {
		t.Fatalf("the stage stopped on its node: %v %v", in, err)
	}
	if !a.formations.Live(f.GetCacheKey()) {
		t.Fatal("the model's tensor cache stays wanted while a formation of the model is known")
	}
	if a.formations.Live(f.GetId()) {
		t.Fatal("a stopped formation's own directory is not wanted")
	}
}

// A seat that fails to reach ready fails the formation, whose record keeps the seat's error and
// triage; the head never launches; the seat's node carries the failure on its record until the
// conductor's record has taken it
func TestSeatFailureKeepsErrorAndTriage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := mesh2(t, ctx)
	stored := a.storeModel("src", "org/model", "q4")
	f, _, err := a.formations.StartPlanned(ctx, runOf(map[string]string{fake.ParamFailRole: runtimes.RoleStage}), formations.Planned{Plan: chainPlan(a, b), Stored: stored, Rt: fake.Runtime{}, Name: modelKey})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the formation fails", func() bool {
		f, err = a.formations.Get(f.GetId())
		return err == nil && f.GetState() == v1.FormationState_FORMATION_STATE_FAILED
	})
	stage := seatByRole(f, runtimes.RoleStage)
	if stage.GetState() != v1.InstanceState_INSTANCE_STATE_FAILED || stage.GetError() == "" {
		t.Fatalf("the stage seat keeps its error: %v", stage)
	}
	if len(stage.GetTriage()) == 0 || stage.GetTriage()[0].GetId() != "boom" {
		t.Fatalf("the stage seat keeps its triage: %v", stage.GetTriage())
	}
	if !strings.Contains(f.GetError(), "the fake process went boom") {
		t.Fatalf("the formation's error names the triage: %s", f.GetError())
	}
	if len(a.launcher.Order()) != 0 {
		t.Fatalf("the head never launched: %v", a.launcher.Order())
	}
	// The seat's node advertises the failure while the conductor's record has not taken it, and
	// drops it once the record has.
	newer := proto.Clone(f).(*v1.Formation)
	newer.Sequence, newer.State = f.GetSequence()+10, v1.FormationState_FORMATION_STATE_STARTING
	for _, s := range newer.Seats {
		s.State, s.Error, s.Triage = v1.InstanceState_INSTANCE_STATE_STARTING, "", nil
	}
	b.formations.Merge(a.mesh.Self(), []*v1.Formation{newer})
	carried := false
	for _, ref := range b.formations.Seats() {
		if ref.GetInstanceId() == stage.GetInstanceId() && ref.GetState() == v1.InstanceState_INSTANCE_STATE_FAILED && len(ref.GetTriage()) > 0 && ref.GetError() != "" {
			carried = true
		}
	}
	if !carried {
		t.Fatalf("the failed seat is carried with its error and triage: %v", b.formations.Seats())
	}
	taken := proto.Clone(newer).(*v1.Formation)
	taken.Sequence++
	for _, s := range taken.Seats {
		if s.GetInstanceId() == stage.GetInstanceId() {
			s.State = v1.InstanceState_INSTANCE_STATE_FAILED
		}
	}
	b.formations.Merge(a.mesh.Self(), []*v1.Formation{taken})
	for _, ref := range b.formations.Seats() {
		if ref.GetInstanceId() == stage.GetInstanceId() {
			t.Fatalf("a failure the conductor took is no longer advertised: %v", ref)
		}
	}
}

// A stop during the launch cancels the launch task, which stops the seat it started, and the
// head never launches
func TestStopDuringLaunchCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := mesh2(t, ctx)
	stored := a.storeModel("src", "org/model", "q4")
	f, task, err := a.formations.StartPlanned(ctx, runOf(map[string]string{fake.ParamSlowRole: runtimes.RoleStage}), formations.Planned{Plan: chainPlan(a, b), Stored: stored, Rt: fake.Runtime{}, Name: modelKey})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the stage launches", func() bool { return b.launcher.Proc(modelKey+"/stage1") != nil })
	stopped, err := a.formations.Stop(ctx, f.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if stopped.GetState() != v1.FormationState_FORMATION_STATE_STOPPED {
		t.Fatalf("stopped %v", stopped)
	}
	done, _, err := a.tasks.Get(task.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if done.GetState() != v1.TaskState_TASK_STATE_CANCELED {
		t.Fatalf("the launch task is canceled: %v", done)
	}
	stage := seatByRole(stopped, runtimes.RoleStage)
	if in, err := b.instances.Get(stage.GetInstanceId()); err != nil || !instances.Terminal(in.GetState()) {
		t.Fatalf("the stage the launch started is stopped: %v %v", in, err)
	}
	if len(a.launcher.Order()) != 0 {
		t.Fatalf("the head never launched: %v", a.launcher.Order())
	}
}

// A seat leaving ready while serving degrades the formation, stops the rest, and launches it
// again after the delay with the same tensor cache on the stage
func TestDegradeRelaunchesWithTheSameCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer formations.SetRelaunchDelay(200 * time.Millisecond)()
	a, b := mesh2(t, ctx)
	stored := a.storeModel("src", "org/model", "q4")
	a.formations.SetPlanner(fixedPlanner(chainPlan(a, b), stored))
	f, _, err := a.formations.StartPlanned(ctx, runOf(nil), formations.Planned{Plan: chainPlan(a, b), Stored: stored, Rt: fake.Runtime{}, Name: modelKey})
	if err != nil {
		t.Fatal(err)
	}
	f = waitFormation(t, a, f.GetId(), v1.FormationState_FORMATION_STATE_READY)
	firstStage, err := b.instances.Get(seatByRole(f, runtimes.RoleStage).GetInstanceId())
	if err != nil {
		t.Fatal(err)
	}
	if !b.launcher.Crash(modelKey + "/stage1") {
		t.Fatal("no stage to crash")
	}
	eventually(t, "the formation fails after its stage died", func() bool {
		old, err := a.formations.Get(f.GetId())
		return err == nil && old.GetState() == v1.FormationState_FORMATION_STATE_FAILED && strings.Contains(old.GetError(), "left ready")
	})
	var fresh *v1.Formation
	eventually(t, "the formation launches again and is ready", func() bool {
		fresh, err = a.formations.Get(modelKey)
		return err == nil && fresh.GetId() != f.GetId() && fresh.GetState() == v1.FormationState_FORMATION_STATE_READY
	})
	if fresh.GetCacheKey() != f.GetCacheKey() {
		t.Fatalf("the relaunch keeps the model's cache key: %s then %s", f.GetCacheKey(), fresh.GetCacheKey())
	}
	secondStage, err := b.instances.Get(seatByRole(fresh, runtimes.RoleStage).GetInstanceId())
	if err != nil {
		t.Fatal(err)
	}
	if secondStage.GetSeat().GetCacheDir() != firstStage.GetSeat().GetCacheDir() {
		t.Fatalf("the stage finds its cache where it left it: %s then %s", firstStage.GetSeat().GetCacheDir(), secondStage.GetSeat().GetCacheDir())
	}
	if in, err := a.instances.Get(seatByRole(f, runtimes.RoleHead).GetInstanceId()); err != nil || !instances.Terminal(in.GetState()) {
		t.Fatalf("the first head was stopped with its formation: %v %v", in, err)
	}
}

// A copy of another conductor's formation takes a higher sequence only, and the same sequence
// again after this node marked the copy unreachable
func TestMergeTakesTheHigherSequence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := mesh2(t, ctx)
	record := func(seq uint64, state v1.FormationState) *v1.Formation {
		return &v1.Formation{Id: "f-merge", Name: "merged", Conductor: a.mesh.Self(), ConductorName: "alpha", Sequence: seq, State: state, CreatedAt: timestamppb.Now()}
	}
	b.formations.Merge(a.mesh.Self(), []*v1.Formation{record(5, v1.FormationState_FORMATION_STATE_READY)})
	b.formations.Merge(a.mesh.Self(), []*v1.Formation{record(4, v1.FormationState_FORMATION_STATE_FAILED)})
	got, err := b.formations.Get("f-merge")
	if err != nil || got.GetSequence() != 5 || got.GetState() != v1.FormationState_FORMATION_STATE_READY {
		t.Fatalf("a lower sequence is not taken whatever its state: %v %v", got, err)
	}
	b.formations.Merge(a.mesh.Self(), []*v1.Formation{record(5, v1.FormationState_FORMATION_STATE_STOPPED)})
	if got, _ = b.formations.Get("f-merge"); got.GetState() != v1.FormationState_FORMATION_STATE_READY {
		t.Fatalf("the same sequence is not taken over a copy the conductor wrote: %v", got)
	}
	b.formations.MemberState(a.mesh.Self(), v1.NodeState_NODE_STATE_UNREACHABLE)
	if got, _ = b.formations.Get("f-merge"); got.GetState() != v1.FormationState_FORMATION_STATE_UNREACHABLE {
		t.Fatalf("a conductor gone quiet leaves its formation unreachable here: %v", got)
	}
	b.formations.Merge(a.mesh.Self(), []*v1.Formation{record(5, v1.FormationState_FORMATION_STATE_READY)})
	if got, _ = b.formations.Get("f-merge"); got.GetState() != v1.FormationState_FORMATION_STATE_READY {
		t.Fatalf("the conductor's record at the same sequence wins over the unreachable mark: %v", got)
	}
	b.formations.Merge(a.mesh.Self(), []*v1.Formation{record(6, v1.FormationState_FORMATION_STATE_STOPPED)})
	if got, _ = b.formations.Get("f-merge"); got.GetSequence() != 6 || got.GetState() != v1.FormationState_FORMATION_STATE_STOPPED {
		t.Fatalf("a higher sequence is taken: %v", got)
	}
}

// A seat's node stops a seat whose conductor reports the formation ended, and one whose
// conductor is gone
func TestSeatNodeReapsSeats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := mesh2(t, ctx)
	stored := a.storeModel("src", "org/model", "q4")
	a.formations.SetPlanner(func(context.Context, *v1.RunRequest) (formations.Planned, error) {
		return formations.Planned{}, context.Canceled
	})
	f, _, err := a.formations.StartPlanned(ctx, runOf(nil), formations.Planned{Plan: chainPlan(a, b), Stored: stored, Rt: fake.Runtime{}, Name: modelKey})
	if err != nil {
		t.Fatal(err)
	}
	f = waitFormation(t, a, f.GetId(), v1.FormationState_FORMATION_STATE_READY)
	stage := seatByRole(f, runtimes.RoleStage)
	ended := proto.Clone(f).(*v1.Formation)
	ended.Sequence, ended.State = f.GetSequence()+100, v1.FormationState_FORMATION_STATE_STOPPED
	b.formations.Merge(a.mesh.Self(), []*v1.Formation{ended})
	eventually(t, "the seat of an ended formation is reaped", func() bool {
		in, err := b.instances.Get(stage.GetInstanceId())
		return err == nil && in.GetState() == v1.InstanceState_INSTANCE_STATE_STOPPED
	})
	eventually(t, "the conductor sees its seat gone", func() bool {
		old, err := a.formations.Get(f.GetId())
		return err == nil && old.GetState() == v1.FormationState_FORMATION_STATE_FAILED
	})
	// A second formation's seat outlives nothing when its conductor is gone for good.
	second := seatOn(b, runtimes.RoleStage, 1, 1)
	run := runOf(nil)
	run.Name = "second"
	g, _, err := a.formations.StartPlanned(ctx, run, formations.Planned{Plan: planOf(v1.Shape_SHAPE_CHAIN, second, seatOn(a, runtimes.RoleHead, 0, 2)), Stored: stored, Rt: fake.Runtime{}, Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	g = waitFormation(t, a, g.GetId(), v1.FormationState_FORMATION_STATE_READY)
	b.formations.MemberState(a.mesh.Self(), v1.NodeState_NODE_STATE_GONE)
	eventually(t, "the seat of a gone conductor is reaped", func() bool {
		in, err := b.instances.Get(seatByRole(g, runtimes.RoleStage).GetInstanceId())
		return err == nil && in.GetState() == v1.InstanceState_INSTANCE_STATE_STOPPED
	})
}

// A conductor restarting adopts the head and verifies every seat over the sync before the
// formation is ready again; a formation being stopped when it went down ends through the slots;
// and a head that did not survive makes the formation launch again
func TestRecoverVerifiesEverySeat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := mesh2(t, ctx)
	stored := a.storeModel("src", "org/model", "q4")
	f, _, err := a.formations.StartPlanned(ctx, runOf(nil), formations.Planned{Plan: chainPlan(a, b), Stored: stored, Rt: fake.Runtime{}, Name: modelKey})
	if err != nil {
		t.Fatal(err)
	}
	f = waitFormation(t, a, f.GetId(), v1.FormationState_FORMATION_STATE_READY)
	// A formation a previous daemon was stopping, with a slot, ends through the slots on recovery.
	stopping := &v1.Formation{Id: "f-stopping", Name: "stopping", Conductor: a.mesh.Self(), SlotId: "slot-1", State: v1.FormationState_FORMATION_STATE_STOPPING, Sequence: 3, CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now(), Request: &v1.RunRequest{}}
	if err := a.db.PutFormation(ctx, stopping); err != nil {
		t.Fatal(err)
	}
	a.formations.Close()
	fresh := a.newFormations()
	slots := &slotLog{ch: make(chan *v1.Formation, 16)}
	fresh.SlotView = slots
	if err := fresh.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	fresh.Start()
	adopted, err := fresh.Get(f.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if adopted.GetState() != v1.FormationState_FORMATION_STATE_STARTING || !strings.Contains(adopted.GetError(), "verifying") {
		t.Fatalf("a recovered formation starts by verifying its seats: %v", adopted)
	}
	adopted = waitFormation(t, a, f.GetId(), v1.FormationState_FORMATION_STATE_READY)
	if adopted.GetId() != f.GetId() || adopted.GetError() != "" {
		t.Fatalf("the same formation is ready again: %v", adopted)
	}
	for _, s := range adopted.GetSeats() {
		if s.GetState() != v1.InstanceState_INSTANCE_STATE_READY {
			t.Fatalf("every seat verified: %v", s)
		}
	}
	if route, ok := a.routes.Lookup(modelKey); !ok || route.GetFormationId() != f.GetId() {
		t.Fatalf("the route returns after every seat verified: %v", route)
	}
	if len(a.launcher.Order()) != 1 || len(b.launcher.Order()) != 1 {
		t.Fatalf("no seat launched again: %v %v", a.launcher.Order(), b.launcher.Order())
	}
	eventually(t, "the slots hear the stopping formation end", func() bool {
		for {
			select {
			case ev := <-slots.ch:
				if ev.GetId() == "f-stopping" && ev.GetState() == v1.FormationState_FORMATION_STATE_STOPPED {
					return true
				}
			default:
				return false
			}
		}
	})
	// The head dies while the conductor is down: the recovery finds it gone and launches again.
	fresh.Close()
	if _, err := a.instances.Stop(ctx, seatByRole(f, runtimes.RoleHead).GetInstanceId()); err != nil {
		t.Fatal(err)
	}
	again := a.newFormations()
	again.SetPlanner(fixedPlanner(chainPlan(a, b), stored))
	if err := again.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	again.Start()
	var relaunched *v1.Formation
	eventually(t, "the formation launches again after its head died", func() bool {
		relaunched, err = again.Get(modelKey)
		return err == nil && relaunched.GetId() != f.GetId() && relaunched.GetState() == v1.FormationState_FORMATION_STATE_READY
	})
	old, err := again.Get(f.GetId())
	if err != nil || old.GetState() != v1.FormationState_FORMATION_STATE_STOPPED || !strings.Contains(old.GetError(), db.RestartNote) {
		t.Fatalf("the old formation ended with the restart note: %v %v", old, err)
	}
}

// Ranks rendezvous at an address the head's node allocates, every rank launching with it, and a
// relaunch meets at the same address with the same ranks
func TestRendezvousAtTheHeadNodeAndReused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer formations.SetRelaunchDelay(200 * time.Millisecond)()
	a, b := mesh2(t, ctx)
	stored := a.storeModel("src", "org/model", "q4")
	b.storeModel("src", "org/model", "q4")
	plan := planOf(v1.Shape_SHAPE_LOCKSTEP, seatOn(b, runtimes.RoleHead, 0, 1), seatOn(a, runtimes.RoleRank, 1, 1))
	a.formations.SetPlanner(fixedPlanner(plan, stored))
	f, _, err := a.formations.StartPlanned(ctx, runOf(nil), formations.Planned{Plan: proto.Clone(plan).(*v1.FormationPlan), Stored: stored, Rt: fake.Runtime{}, Name: modelKey})
	if err != nil {
		t.Fatal(err)
	}
	f = waitFormation(t, a, f.GetId(), v1.FormationState_FORMATION_STATE_READY)
	if !strings.HasPrefix(f.GetRendezvous(), "127.0.0.1:") || strings.HasSuffix(f.GetRendezvous(), ":0") {
		t.Fatalf("the rendezvous is allocated on the head's node: %q", f.GetRendezvous())
	}
	head, err := b.instances.Get(seatByRole(f, runtimes.RoleHead).GetInstanceId())
	if err != nil {
		t.Fatal(err)
	}
	rank, err := a.instances.Get(seatByRole(f, runtimes.RoleRank).GetInstanceId())
	if err != nil {
		t.Fatal(err)
	}
	if head.GetSeat().GetRendezvous() != f.GetRendezvous() || rank.GetSeat().GetRendezvous() != f.GetRendezvous() {
		t.Fatalf("every rank launches with the rendezvous: head %q rank %q", head.GetSeat().GetRendezvous(), rank.GetSeat().GetRendezvous())
	}
	if rank.GetSeat().GetExposed() || rank.GetSeat().GetPort() == rank.GetSeat().GetLocalPort() {
		t.Fatalf("a rank sits behind a guard listener under guard exposure: %v", rank.GetSeat())
	}
	if !b.launcher.Proc(modelKey + "/head0").LaunchedAt.Before(a.launcher.Proc(modelKey + "/rank1").LaunchedAt) {
		t.Fatal("the head launches first, so its node allocates the rendezvous the others use")
	}
	if !a.launcher.Crash(modelKey + "/rank1") {
		t.Fatal("no rank to crash")
	}
	var fresh *v1.Formation
	eventually(t, "the formation launches again", func() bool {
		fresh, err = a.formations.Get(modelKey)
		return err == nil && fresh.GetId() != f.GetId() && fresh.GetState() == v1.FormationState_FORMATION_STATE_READY
	})
	if fresh.GetRendezvous() != f.GetRendezvous() {
		t.Fatalf("the relaunch meets at the same address: %q then %q", f.GetRendezvous(), fresh.GetRendezvous())
	}
	for _, s := range fresh.GetSeats() {
		before := seatByRole(f, s.GetRole())
		if before.GetRank() != s.GetRank() || before.GetNodeId() != s.GetNodeId() {
			t.Fatalf("the relaunch keeps the ranks: %v then %v", f.GetSeats(), fresh.GetSeats())
		}
	}
}

// A relay whose decode role wants a canary is ready only after one completion went through the
// pair, and fails when the prefill seat hands nothing off
func TestRelayCanary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := mesh2(t, ctx)
	stored := a.storeModel("src", "org/model", "q4")
	b.storeModel("src", "org/model", "q4")
	plan := planOf(v1.Shape_SHAPE_RELAY, seatOn(b, runtimes.RolePrefill, 0, 1), seatOn(a, runtimes.RoleDecode, 1, 1))
	f, task, err := a.formations.StartPlanned(ctx, runOf(nil), formations.Planned{Plan: proto.Clone(plan).(*v1.FormationPlan), Stored: stored, Rt: fake.Runtime{}, Name: modelKey})
	if err != nil {
		t.Fatal(err)
	}
	waitFormation(t, a, f.GetId(), v1.FormationState_FORMATION_STATE_READY)
	if b.launcher.Proc(modelKey+"/prefill0").Chats.Load() == 0 {
		t.Fatal("the canary went through the prefill seat")
	}
	if _, err := a.tasks.Wait(ctx, task.GetId()); err != nil {
		t.Fatal(err)
	}
	_, logs, err := a.tasks.Get(task.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "canary answered through the relay pair") {
		t.Fatalf("the task says the canary answered: %v", logs)
	}
	if _, err := a.formations.Stop(ctx, f.GetId()); err != nil {
		t.Fatal(err)
	}
	run := runOf(map[string]string{fake.ParamFailRole: fake.FailHandoff})
	run.Name = "broken"
	g, _, err := a.formations.StartPlanned(ctx, run, formations.Planned{Plan: proto.Clone(plan).(*v1.FormationPlan), Stored: stored, Rt: fake.Runtime{}, Name: "broken"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "a relay whose handoff does not happen fails", func() bool {
		g, err = a.formations.Get(g.GetId())
		return err == nil && g.GetState() == v1.FormationState_FORMATION_STATE_FAILED
	})
	if !strings.Contains(g.GetError(), "canary") {
		t.Fatalf("the formation's error names the canary: %s", g.GetError())
	}
	if _, ok := a.routes.Lookup("broken"); ok {
		t.Fatal("a formation whose canary failed is not routed")
	}
}

// A head holding parts has every part the request names pulled to its node, one progress row
// for the seat counting each file
func TestPartsReachTheHead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := mesh2(t, ctx)
	stored := a.storeModel("src", "org/model", "q4")
	b.storeModel("src", "org/model", "q4")
	b.storeModel("src", "org/part", "p")
	eventually(t, "the conductor knows what the head's node holds", func() bool {
		rec, err := a.mesh.Node(b.mesh.Self())
		return err == nil && len(rec.GetStored()) == 2
	})
	plan := planOf(v1.Shape_SHAPE_STAGES, seatOn(a, runtimes.RoleDenoiser, 1, 1), seatOn(b, runtimes.RoleHead, 0, 2))
	f, task, err := a.formations.StartPlanned(ctx, runOf(map[string]string{fake.ParamExtraPart: runtimes.StoreScheme + "src/org/part#p"}), formations.Planned{Plan: plan, Stored: stored, Rt: fake.Runtime{}, Name: modelKey})
	if err != nil {
		t.Fatal(err)
	}
	waitFormation(t, a, f.GetId(), v1.FormationState_FORMATION_STATE_READY)
	done, err := a.tasks.Wait(ctx, task.GetId())
	if err != nil {
		t.Fatal(err)
	}
	rows := done.GetProgress().GetRows()
	if len(rows) != 1 || !strings.HasPrefix(rows[0].GetKey(), "head0 on ") || rows[0].GetTotal() != 2 || rows[0].GetDone() != 2 {
		t.Fatalf("one row for the head counting the model and its part: %v", rows)
	}
}

// Seat measurements sum by node and key, and bytes an exposed stage took come from the head's log
func TestSumsAndHeadLog(t *testing.T) {
	seats := []*v1.Seat{
		{NodeId: "n1", NodeName: "one", Measurements: []*v1.Measurement{{Key: "weights", Bytes: 10}, {Key: "cache", Bytes: 1}}},
		{NodeId: "n1", NodeName: "one", Measurements: []*v1.Measurement{{Key: "weights", Bytes: 5}}},
		{NodeId: "n2", NodeName: "two", Measurements: []*v1.Measurement{{Key: "weights", Bytes: 7}}},
	}
	sums := formations.NodeSums(seats)
	if len(sums) != 2 || sums[0].GetNodeId() != "n1" || len(sums[0].GetMeasurements()) != 2 || sums[0].GetMeasurements()[0].GetBytes() != 15 || sums[1].GetMeasurements()[0].GetBytes() != 7 {
		t.Fatalf("sums %v", sums)
	}
	lines := []string{
		"load_tensors:   RPC[10.0.0.2:5000] model buffer size =  4096.00 MiB",
		"load_tensors:   RPC[10.0.0.3:5000] model buffer size =  1.50 GiB",
		"load_tensors:        CUDA0 model buffer size =  2048.00 MiB",
	}
	if n, ok := formations.HeadStreamed(lines, "10.0.0.2:5000"); !ok || n != 4096<<20 {
		t.Fatalf("stage two: %d %v", n, ok)
	}
	if n, ok := formations.HeadStreamed(lines, "10.0.0.3:5000"); !ok || n != 3<<29 {
		t.Fatalf("stage three: %d %v", n, ok)
	}
	if _, ok := formations.HeadStreamed(lines, "10.0.0.4:5000"); ok {
		t.Fatal("a stage the log never names is not counted")
	}
}

// A relaunch drops span entries naming members that left the mesh, says so in the plan's detail,
// and plans over what remains
func TestRelaunchDropsMembersThatLeft(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := mesh2(t, ctx)
	stored := a.storeModel("src", "org/model", "q4")
	var planned []string
	a.formations.SetPlanner(func(ctx context.Context, run *v1.RunRequest) (formations.Planned, error) {
		planned = append([]string(nil), run.GetSpan()...)
		return formations.Planned{Plan: chainPlan(a, b), Stored: stored, Rt: fake.Runtime{}, Name: modelKey}, nil
	})
	prev := &v1.Formation{Id: "f-old", Name: modelKey, Conductor: a.mesh.Self(), State: v1.FormationState_FORMATION_STATE_FAILED, Request: runOf(nil)}
	prev.Request.Span = []string{a.mesh.Self(), b.mesh.Self() + "/cuda0", "deadbeef/cuda0"}
	f, _, err := a.formations.Relaunch(ctx, prev)
	if err != nil {
		t.Fatal(err)
	}
	if len(planned) != 2 || planned[0] != a.mesh.Self() || planned[1] != b.mesh.Self()+"/cuda0" {
		t.Fatalf("the planner sees the span without the member that left: %v", planned)
	}
	if !strings.Contains(f.GetPlan().GetDetail(), "deadbeef left the mesh and dropped out of the span") {
		t.Fatalf("the plan says who dropped out: %s", f.GetPlan().GetDetail())
	}
	waitFormation(t, a, f.GetId(), v1.FormationState_FORMATION_STATE_READY)
}

// A seat whose log names an RDMA transport is exposed on that link, and the record says so
func TestRDMATransportMarksSeatsExposed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := mesh2(t, ctx)
	stored := a.storeModel("src", "org/model", "q4")
	f, _, err := a.formations.StartPlanned(ctx, runOf(map[string]string{fake.ParamTransport: "rdma"}), formations.Planned{Plan: chainPlan(a, b), Stored: stored, Rt: fake.Runtime{}, Name: modelKey})
	if err != nil {
		t.Fatal(err)
	}
	f = waitFormation(t, a, f.GetId(), v1.FormationState_FORMATION_STATE_READY)
	for _, s := range f.GetSeats() {
		if s.GetTransport() != "rdma" || !s.GetExposed() {
			t.Fatalf("a seat that negotiated RDMA is exposed: %v", s)
		}
	}
	stage, err := b.instances.Get(seatByRole(f, runtimes.RoleStage).GetInstanceId())
	if err != nil {
		t.Fatal(err)
	}
	if stage.GetSeat().GetExposed() {
		t.Fatal("under guard exposure the process itself still sits behind the guard")
	}
}
