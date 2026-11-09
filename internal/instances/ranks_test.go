package instances

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/launch"
	"github.com/nickheyer/nebu/pkg/proc"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The process names of every rank of a launch named name
func rankNames(name string, ranks int) []string {
	out := make([]string, ranks)
	for i := range out {
		out[i] = fake.RankName(name, i)
	}
	return out
}

// Several data parallel ranks run one process each on its own port and publish one endpoint each
func TestRanksPublishEachPort(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		ranks  int
		grpc   bool
	}{
		{name: "one rank keeps one endpoint", params: nil, ranks: 1},
		{name: "three ranks on consecutive ports", params: map[string]string{fake.ParamRanks: "3", fake.ParamMaxSeqs: "4"}, ranks: 3},
		{name: "two gRPC ranks two ports apart", params: map[string]string{fake.ParamRanks: "2", fake.ParamGRPC: "true"}, ranks: 2, grpc: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			n := newSeatNode(t, ctx)
			m := n.manager(t, ctx, nil)
			in, _, err := m.Run(ctx, soloRun(c.params))
			if err != nil {
				t.Fatal(err)
			}
			ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
			base := portOf(t, ready.GetEndpoint())
			step := 1
			if c.grpc {
				step = 2
			}
			refs := rankRefs(ready)
			if len(refs) != c.ranks || (c.ranks == 1) != (len(ready.GetRanks()) == 0) {
				t.Fatalf("ranks %v", ready.GetRanks())
			}
			pids := map[int32]bool{}
			for i, ref := range refs {
				if portOf(t, ref.endpoint) != base+i*step || ref.pid == 0 || pids[ref.pid] || !ref.healthy {
					t.Fatalf("rank %d %+v from base %d", i, ref, base)
				}
				pids[ref.pid] = true
				if c.ranks > 1 && !strings.Contains(strings.Join(ref.command, " "), fmt.Sprintf("--port %d", base+i*step)) {
					t.Fatalf("rank %d command %v", i, ref.command)
				}
			}
			for _, name := range rankNames("solo", c.ranks) {
				if n.launcher.Launches(name) != 1 {
					t.Fatalf("%s launched %d times, launched %v", name, n.launcher.Launches(name), n.launcher.Order())
				}
			}
			owner := ownerOf(in.GetId())
			eps := n.registrar.Endpoints(owner)
			if len(eps) != c.ranks {
				t.Fatalf("published %+v", eps)
			}
			for i, ep := range eps {
				http := base + i*step
				addr := http
				if c.grpc {
					addr = http + 1
				}
				if ep.Rank != uint32(i) || ep.Address.Port != addr || ep.MetricsPort != uint32(http) || ep.HealthPort != uint32(http) || !ep.Ready || ep.InstanceID != in.GetId() || ep.Labels[LabelServed] != "solo" {
					t.Fatalf("rank %d published %+v", i, ep)
				}
				if !n.registrar.RankReady(in.GetId(), uint32(i)) {
					t.Fatalf("rank %d is not ready", i)
				}
			}
			if mb, _ := n.registrar.Membership(owner); c.params[fake.ParamMaxSeqs] != "" && mb.Concurrency != 4 {
				t.Fatalf("each rank runs its own sequences, membership %+v", mb)
			}
			if c.ranks > 1 && !strings.Contains(ConditionOf(ready.GetConditions(), CondLaunched).GetMessage(), "pids ") {
				t.Fatalf("launched condition %v", ConditionOf(ready.GetConditions(), CondLaunched))
			}
			if _, err := m.Stop(ctx, in.GetId()); err != nil {
				t.Fatal(err)
			}
			stopped := slices.Sorted(slices.Values(n.launcher.StopOrder()))
			if !slices.Equal(stopped, slices.Sorted(slices.Values(rankNames("solo", c.ranks)))) {
				t.Fatalf("a stop ends every rank, stopped %v", stopped)
			}
		})
	}
}

// A rank that stops answering health takes only its own endpoint out of traffic while every process keeps running
func TestRankHealthFlipsOnlyItsEndpoint(t *testing.T) {
	for _, dead := range []int{0, 1} {
		t.Run(fmt.Sprintf("rank %d", dead), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			n := newSeatNode(t, ctx)
			m := n.manager(t, ctx, nil)
			in, _, err := m.Run(ctx, soloRun(map[string]string{fake.ParamRanks: "2"}))
			if err != nil {
				t.Fatal(err)
			}
			waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
			other := 1 - dead
			n.launcher.SetHealthy(fake.RankName("solo", dead), false)
			waitFor(t, "the dead rank out of traffic", func() bool {
				return !n.registrar.RankReady(in.GetId(), uint32(dead)) && !current(t, m, in.GetId()).GetRanks()[dead].GetHealthy()
			})
			rec := current(t, m, in.GetId())
			if !n.registrar.RankReady(in.GetId(), uint32(other)) || !rec.GetRanks()[other].GetHealthy() || rec.GetHealthy() {
				t.Fatalf("rank %d ready %v record %v healthy %v", other, n.registrar.RankReady(in.GetId(), uint32(other)), rec.GetRanks(), rec.GetHealthy())
			}
			c := ConditionOf(rec.GetConditions(), CondHealthy)
			if c.GetReason() != ReasonProbeFailed || !strings.Contains(c.GetMessage(), fmt.Sprintf("ranks %d failed", dead)) {
				t.Fatalf("healthy condition %v", c)
			}
			if rec.GetState() != v1.InstanceState_INSTANCE_STATE_READY || len(n.launcher.StopOrder()) != 0 || n.launcher.Launches(fake.RankName("solo", dead)) != 1 {
				t.Fatalf("state %s stops %v, an unhealthy rank keeps its process", rec.GetState(), n.launcher.StopOrder())
			}
			history := n.registrar.History()
			if !slices.Contains(history, fmt.Sprintf("rankready %s %d false", in.GetId(), dead)) || slices.Contains(history, fmt.Sprintf("rankready %s %d false", in.GetId(), other)) {
				t.Fatalf("readiness calls %v", history)
			}
			n.launcher.SetHealthy(fake.RankName("solo", dead), true)
			waitFor(t, "the rank back in traffic", func() bool {
				return n.registrar.RankReady(in.GetId(), uint32(dead)) && current(t, m, in.GetId()).GetHealthy()
			})
			if _, err := m.Stop(ctx, in.GetId()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A rank that exits ends its whole group, which launches again every rank as one crash restart
func TestRankCrashRelaunchesEveryRank(t *testing.T) {
	quickBackoff(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	in, _, err := m.Run(ctx, soloRun(map[string]string{fake.ParamRanks: "2"}))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	sub := m.Events.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_INSTANCE})
	crash := make(chan string, 1)
	go func() {
		for ev := range sub.Events() {
			if e := ev.GetInstance().GetError(); strings.Contains(e, "rank 1") {
				crash <- e
				return
			}
		}
	}()
	if !n.launcher.Crash(fake.RankName("solo", 1)) {
		t.Fatal("no rank to crash")
	}
	waitFor(t, "every rank relaunched", func() bool {
		rec := current(t, m, in.GetId())
		return rec.GetRestarts() == 1 && rec.GetState() == v1.InstanceState_INSTANCE_STATE_READY && n.launcher.Launches("solo") == 2 && n.launcher.Launches(fake.RankName("solo", 1)) == 2
	})
	rec := current(t, m, in.GetId())
	if !slices.Contains(n.launcher.StopOrder(), "solo") {
		t.Fatalf("the surviving rank stops with the group, stopped %v", n.launcher.StopOrder())
	}
	if len(rec.GetTriage()) != 0 || len(n.registrar.Endpoints(ownerOf(in.GetId()))) != 2 {
		t.Fatalf("triage %v endpoints %v", rec.GetTriage(), n.registrar.Endpoints(ownerOf(in.GetId())))
	}
	if e := <-crash; !strings.Contains(e, "rank 1: signal: killed") {
		t.Fatalf("the crash names the rank, error %q", e)
	}
	weights := 0
	for _, line := range m.logOf(mustFind(t, m, in.GetId())).Tail(0) {
		if strings.HasPrefix(line, "weights=") {
			weights++
		}
	}
	if weights != 2 {
		t.Fatalf("the group log holds both ranks' lines, %d weight lines", weights)
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// Adapters load on every rank and scope every rank's endpoint, and sleep and wake reach every rank
func TestRankAdaptersAndSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	run := soloRun(map[string]string{fake.ParamRanks: "2", fake.ParamLoRA: "true", fake.ParamSleep: "true"})
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	owner := ownerOf(in.GetId())
	if err := m.LoadAdapter(ctx, in.GetId(), "sql", "/adapters/sql"); err != nil {
		t.Fatal(err)
	}
	eps := n.registrar.Endpoints(owner)
	if len(eps) != 4 {
		t.Fatalf("published %+v", eps)
	}
	for _, rank := range []uint32{0, 1} {
		var base, scoped bool
		for _, ep := range eps {
			if ep.Rank != rank {
				continue
			}
			base = base || ep.Adapter == ""
			scoped = scoped || ep.Adapter == "sql" && ep.Labels[datalayer.LabelAdapter] == "sql"
		}
		if !base || !scoped {
			t.Fatalf("rank %d published %+v", rank, eps)
		}
	}
	for _, ref := range rankRefs(ready) {
		resp, err := http.Get(ref.endpoint + "/v1/models")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if err := m.UnloadAdapter(ctx, in.GetId(), "sql"); err != nil {
		t.Fatal(err)
	}
	if eps := n.registrar.Endpoints(owner); len(eps) != 2 {
		t.Fatalf("after unload %+v", eps)
	}
	sleeper := sleeperOf(fake.Runtime{}, ready)
	if err := sleeper.Sleep(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range rankNames("solo", 2) {
		if p := n.launcher.Proc(name); !p.Asleep() || p.Sleeps.Load() != 1 {
			t.Fatalf("%s asleep %v sleeps %d", name, p.Asleep(), p.Sleeps.Load())
		}
	}
	if err := sleeper.Wake(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range rankNames("solo", 2) {
		if p := n.launcher.Proc(name); p.Asleep() || p.Wakes.Load() != 1 {
			t.Fatalf("%s asleep %v wakes %d", name, p.Asleep(), p.Wakes.Load())
		}
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// The processes and record a previous daemon left for a run of two ranks, the dead rank's process already gone
func leftRanks(t *testing.T, ctx context.Context, n *seatNode, dead int) *v1.Instance {
	t.Helper()
	request := soloRun(map[string]string{fake.ParamRanks: "2"})
	params, err := runtimes.Resolve(fake.Runtime{}, request.GetParams())
	if err != nil {
		t.Fatal(err)
	}
	rng, err := ephemeralRange()
	if err != nil {
		t.Fatal(err)
	}
	book := proc.NewReservations(bindHost)
	base, err := book.Reserve(rng, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := book.Yield(base); err != nil {
		t.Fatal(err)
	}
	cmds, err := renderRanks(runtimes.Launch{Name: "solo", Params: params, Host: bindHost, Port: base}, 2, fake.Runtime{}.Launch, func(runtimes.Launch, *runtimes.Command) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	id := db.NewID()
	ranks := rankRecords(cmds, bindHost, nil)
	for i, cmd := range cmds {
		if i == dead {
			gone := exec.Command("true")
			if err := gone.Run(); err != nil {
				t.Fatal(err)
			}
			ranks[i].Pid = int32(gone.Process.Pid)
			continue
		}
		h, err := n.launcher.Launch(ctx, launch.Spec{Command: cmd.Command, Args: cmd.Args, Env: cmd.Env, LogPath: (&Manager{Dir: filepath.Join(n.dir, "instances")}).rankLogPath(id, i)})
		if err != nil {
			t.Fatal(err)
		}
		ranks[i].Pid, ranks[i].Healthy = int32(h.Pid()), true
	}
	rec := &v1.Instance{
		Id: id, Name: "solo", SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, InstallId: n.install.GetId(),
		State: v1.InstanceState_INSTANCE_STATE_READY, DesiredRunning: true, Generation: 1, ObservedGeneration: 1, Request: request, Healthy: true,
		Params: cmds[0].Params, Endpoint: ranks[0].GetEndpoint(), Command: ranks[0].GetCommand(), Pid: ranks[0].GetPid(), Ranks: ranks, CreatedAt: timestamppb.Now(),
	}
	if err := n.db.PutInstance(ctx, rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// A restarted daemon adopts every live rank, or stops the survivors of a broken group and launches it whole
func TestRanksAdoptedAfterRestart(t *testing.T) {
	cases := []struct {
		name     string
		dead     int
		launches map[string]int
	}{
		{name: "every rank alive", dead: -1, launches: map[string]int{"solo": 1, "solo/dp1": 1}},
		{name: "rank 1 gone", dead: 1, launches: map[string]int{"solo": 2, "solo/dp1": 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			n := newSeatNode(t, ctx)
			left := leftRanks(t, ctx, n, c.dead)
			m := n.manager(t, ctx, nil)
			if err := m.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			again := waitState(t, m, left.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
			for name, want := range c.launches {
				if got := n.launcher.Launches(name); got != want {
					t.Fatalf("%s launched %d times, want %d", name, got, want)
				}
			}
			for i, rr := range again.GetRanks() {
				adopted := rr.GetPid() == left.GetRanks()[i].GetPid()
				if adopted != (c.dead < 0) || !rr.GetHealthy() {
					t.Fatalf("rank %d %v from %v", i, rr, left.GetRanks()[i])
				}
			}
			if len(n.registrar.Endpoints(ownerOf(left.GetId()))) != 2 {
				t.Fatalf("published %v", n.registrar.Endpoints(ownerOf(left.GetId())))
			}
			profile := &v1.HostProfile{}
			for _, rr := range again.GetRanks() {
				profile.Processes = append(profile.Processes, &v1.ProcessUse{Pid: rr.GetPid()})
			}
			m.Attribute(profile)
			for _, pu := range profile.GetProcesses() {
				if pu.GetInstanceId() != left.GetId() {
					t.Fatalf("process %d attributed to %q", pu.GetPid(), pu.GetInstanceId())
				}
			}
			if _, err := m.Stop(ctx, left.GetId()); err != nil {
				t.Fatal(err)
			}
			if lines := m.logOf(mustFind(t, m, left.GetId())).Tail(0); len(lines) == 0 {
				t.Fatal("a stopped run of ranks keeps its saved log")
			}
		})
	}
}

// A seat of several ranks reached through guards binds one guard per rank and publishes ranks after earlier replicas
func TestSeatRanksGuardEachPort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	run := &v1.RunRequest{
		SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, Name: "reps/replica1", Params: map[string]string{fake.ParamRanks: "2"},
		Seat: &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_REPLICAS, Role: "replica", Rank: 1, Conductor: "conductor", Name: "reps", Address: "127.0.0.1", Admit: []string{"127.0.0.1"}},
	}
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	guards := mustFind(t, m, in.GetId()).guardsOf()
	if len(guards) != 2 || len(ready.GetRanks()) != 2 || ready.GetSeat().GetPort() != uint32(guards[0].Port()) {
		t.Fatalf("guards %d ranks %v seat port %d", len(guards), ready.GetRanks(), ready.GetSeat().GetPort())
	}
	for i, rr := range ready.GetRanks() {
		if rr.GetPort() != uint32(guards[i].Port()) {
			t.Fatalf("rank %d mesh port %d, guard %d", i, rr.GetPort(), guards[i].Port())
		}
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", rr.GetPort()))
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("rank %d through its guard: %v %v", i, resp, err)
		}
		resp.Body.Close()
	}
	if got := []uint32{PublishedRank(ready.GetSeat(), 2, 0), PublishedRank(ready.GetSeat(), 2, 1)}; !slices.Equal(got, []uint32{2, 3}) {
		t.Fatalf("published ranks %v", got)
	}
	refs := []*v1.SeatRef{}
	for _, ref := range m.SeatRefs() {
		if ref.GetInstanceId() == in.GetId() {
			refs = append(refs, ref)
		}
	}
	if len(refs) != 1 || len(refs[0].GetRanks()) != 2 {
		t.Fatalf("seat refs %v", refs)
	}
	n.launcher.SetHealthy(fake.RankName("reps/replica1", 1), false)
	waitFor(t, "the seat rank out of traffic", func() bool {
		return slices.Contains(n.registrar.History(), fmt.Sprintf("rankready %s 3 false", in.GetId()))
	})
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
	if len(mustFind(t, m, in.GetId()).guardsOf()) != 0 {
		t.Fatal("a stopped seat closes every guard")
	}
}

// Ranks of a seat whose role answers no HTTP are refused before anything launches
func TestRanksRefusedForHelloSeats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	run := seatRun("stage", 0, false, n.dir)
	run.Params = map[string]string{fake.ParamRanks: "2"}
	in, task, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	final, err := m.Tasks.Wait(ctx, task.GetId())
	if err != nil || final.GetState() != v1.TaskState_TASK_STATE_FAILED || !strings.Contains(final.GetError(), "answers no HTTP health") {
		t.Fatalf("a hello seat with ranks: %v %q %v", final.GetState(), final.GetError(), err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_FAILED)
	if len(n.launcher.Order()) != 0 {
		t.Fatalf("launched %v", n.launcher.Order())
	}
}

var _ pools.Registrar = (*fake.Registrar)(nil)

// A change to fewer ranks stops the dropped ranks, forgets their records and logs, and publishes the rest
func TestApplyFewerRanks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	in, _, err := m.Run(ctx, soloRun(map[string]string{fake.ParamRanks: "2"}))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if logs := m.rankLogs(in.GetId()); len(logs) != 1 {
		t.Fatalf("rank logs %v", logs)
	}
	if _, err := m.Apply(ctx, in.GetId(), soloRun(nil)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run of one rank", func() bool {
		rec := current(t, m, in.GetId())
		return rec.GetObservedGeneration() == 2 && rec.GetState() == v1.InstanceState_INSTANCE_STATE_READY
	})
	rec := current(t, m, in.GetId())
	if len(rec.GetRanks()) != 0 || len(m.rankLogs(in.GetId())) != 0 || len(n.registrar.Endpoints(ownerOf(in.GetId()))) != 1 || !slices.Contains(n.launcher.StopOrder(), fake.RankName("solo", 1)) {
		t.Fatalf("ranks %v logs %v endpoints %v stopped %v", rec.GetRanks(), m.rankLogs(in.GetId()), n.registrar.Endpoints(ownerOf(in.GetId())), n.launcher.StopOrder())
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}
