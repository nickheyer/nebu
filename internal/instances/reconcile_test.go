package instances

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Shrinks the restart backoff so restart tests take milliseconds
func quickBackoff(t *testing.T) {
	t.Helper()
	minimum, maximum, reset := backoffMin, backoffMax, backoffReset
	backoffMin, backoffMax, backoffReset = 10*time.Millisecond, 40*time.Millisecond, time.Hour
	t.Cleanup(func() { backoffMin, backoffMax, backoffReset = minimum, maximum, reset })
}

// A solo run of the fake runtime under a fixed name with the given params
func soloRun(params map[string]string) *v1.RunRequest {
	return &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, Name: "solo", Params: params}
}

// Waits until cond holds, failing with what after twenty seconds
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The instance as the manager holds it now
func current(t *testing.T, m *Manager, id string) *v1.Instance {
	t.Helper()
	rec, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// The port of an endpoint URL
func portOf(t *testing.T, endpoint string) int {
	t.Helper()
	addr, err := addressOf(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return addr.Port
}

// A ready instance is scheduled, launched, healthy, published, routed and redacted, and a crash restarts it on the pinned install
func TestCrashRestartsOnPinnedInstall(t *testing.T) {
	quickBackoff(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	run := soloRun(nil)
	run.Policy = &v1.Policy{MaxInFlight: 4}
	run.RouteProfile = &v1.Profile{ToolServers: []string{"files"}}
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	for _, typ := range []string{CondScheduled, CondLaunched, CondReady, CondHealthy} {
		if !ConditionTrue(ready.GetConditions(), typ) {
			t.Fatalf("condition %s is not true: %v", typ, ready.GetConditions())
		}
	}
	if ready.GetGeneration() != 1 || ready.GetObservedGeneration() != 1 || !ready.GetHealthy() {
		t.Fatalf("generation %d observed %d healthy %v", ready.GetGeneration(), ready.GetObservedGeneration(), ready.GetHealthy())
	}
	if ready.GetRequest().GetInstallId() != n.install.GetId() {
		t.Fatalf("the request pins install %q, want %q", ready.GetRequest().GetInstallId(), n.install.GetId())
	}
	if line := strings.Join(ready.GetCommand(), " "); strings.Contains(line, fake.Token) || !strings.Contains(line, auth.Redacted) {
		t.Fatalf("the recorded command leaks or misses its redaction: %s", line)
	}
	owner := ownerOf(in.GetId())
	eps := n.registrar.Endpoints(owner)
	port := portOf(t, ready.GetEndpoint())
	if len(eps) != 1 || eps[0].Address.Port != port || eps[0].HealthPort != uint32(port) || eps[0].MetricsPort != uint32(port) || eps[0].Labels[LabelServed] != "solo" {
		t.Fatalf("published %+v for endpoint %s", eps, ready.GetEndpoint())
	}
	if mb, ok := n.registrar.Membership(owner); !ok || mb.Protocol != datalayer.AppProtocolHTTP || mb.Metrics.GetRunningRequests() != "vllm:num_requests_running" || mb.Model.Repo != "org/model" {
		t.Fatalf("membership %+v", mb)
	}
	route, ok := n.registrar.Named("solo")
	if !ok || route.Owner != owner || route.Spec.Policy.GetMaxInFlight() != 4 || !slices.Equal(route.Spec.Profile.GetToolServers(), []string{"files"}) {
		t.Fatalf("route %+v", route)
	}
	newer := &v1.Install{Id: db.NewID(), RuntimeId: fake.RuntimeID, Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Path: filepath.Join(n.dir, "fake2"), Dir: n.dir, Version: "2", CreatedAt: timestamppb.New(time.Now().Add(time.Hour))}
	if err := n.db.PutInstall(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if !n.launcher.Crash("solo") {
		t.Fatal("no process to crash")
	}
	waitFor(t, "the crash restart", func() bool {
		rec := current(t, m, in.GetId())
		return rec.GetRestarts() == 1 && rec.GetState() == v1.InstanceState_INSTANCE_STATE_READY && n.launcher.Launches("solo") == 2
	})
	again := current(t, m, in.GetId())
	if again.GetInstallId() != n.install.GetId() || again.GetRequest().GetInstallId() != n.install.GetId() {
		t.Fatalf("the restart moved to install %q from the pinned %q", again.GetInstallId(), n.install.GetId())
	}
	if again.GetError() != "" || len(n.registrar.Endpoints(owner)) != 1 {
		t.Fatalf("the restart serves again: error %q endpoints %v", again.GetError(), n.registrar.Endpoints(owner))
	}
	history := n.registrar.History()
	if i := slices.Index(history, "unpublish "+in.GetId()); i < 0 || !slices.Contains(history[i:], "publish "+in.GetId()) {
		t.Fatalf("the crash unpublished and the restart published again: %v", history)
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// Five launches in a row that never reach ready fail the instance with the reason and drop its route
func TestFailsAfterFiveLaunches(t *testing.T) {
	quickBackoff(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	in, _, err := m.Run(ctx, soloRun(map[string]string{fake.ParamFail: "true"}))
	if err != nil {
		t.Fatal(err)
	}
	failed := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_FAILED)
	if got := n.launcher.Launches("solo"); got != maxFailedLaunches {
		t.Fatalf("%d launches before failing, want %d", got, maxFailedLaunches)
	}
	if !strings.Contains(failed.GetError(), "5 launches in a row failed") || !strings.Contains(failed.GetError(), "boom") {
		t.Fatalf("error %q", failed.GetError())
	}
	if c := ConditionOf(failed.GetConditions(), CondLaunched); c == nil || c.GetReason() != ReasonFailedLaunches {
		t.Fatalf("launched condition %v", c)
	}
	if _, ok := n.registrar.Named("solo"); ok {
		t.Fatal("a failed instance keeps no route")
	}
}

// A stop drains the owner and waits for requests in flight before the process stops
func TestStopDrainsFirst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	in, _, err := m.Run(ctx, soloRun(nil))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	owner := ownerOf(in.GetId())
	n.traffic.SetInFlight(in.GetId(), 2)
	stopped := make(chan *v1.Instance, 1)
	go func() {
		rec, _ := m.Stop(ctx, in.GetId())
		stopped <- rec
	}()
	waitFor(t, "the drain", func() bool {
		return n.registrar.Drains(owner) == 1 && current(t, m, in.GetId()).GetState() == v1.InstanceState_INSTANCE_STATE_DRAINING
	})
	time.Sleep(200 * time.Millisecond)
	if len(n.launcher.StopOrder()) != 0 {
		t.Fatal("the process stopped with requests in flight")
	}
	if n.registrar.Ready(in.GetId()) {
		t.Fatal("a draining endpoint still takes traffic")
	}
	n.traffic.SetInFlight(in.GetId(), 0)
	rec := <-stopped
	if rec.GetState() != v1.InstanceState_INSTANCE_STATE_STOPPED || rec.GetDesiredRunning() {
		t.Fatalf("state %s desired %v", rec.GetState(), rec.GetDesiredRunning())
	}
	history := n.registrar.History()
	drain, unpublish := slices.Index(history, "drain "+in.GetId()), slices.Index(history, "unpublish "+in.GetId())
	if drain < 0 || unpublish < drain {
		t.Fatalf("drained before unpublishing: %v", history)
	}
	if _, ok := n.registrar.Named("solo"); ok {
		t.Fatal("a stopped instance keeps no route")
	}
}

// A process that exits while draining with an unclean status is recorded as a crash
func TestExitWhileDrainingIsACrash(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	in, _, err := m.Run(ctx, soloRun(nil))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	n.traffic.SetInFlight(in.GetId(), 1)
	go m.Stop(ctx, in.GetId())
	waitFor(t, "the drain", func() bool {
		return current(t, m, in.GetId()).GetState() == v1.InstanceState_INSTANCE_STATE_DRAINING
	})
	n.launcher.Crash("solo")
	failed := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_FAILED)
	if !strings.Contains(failed.GetError(), "exited while draining") {
		t.Fatalf("error %q", failed.GetError())
	}
	if c := ConditionOf(failed.GetConditions(), CondLaunched); c == nil || c.GetReason() != ReasonDrainCrashed {
		t.Fatalf("launched condition %v", c)
	}
	if len(failed.GetTriage()) == 0 || failed.GetTriage()[0].GetId() != "boom" {
		t.Fatalf("triage %v", failed.GetTriage())
	}
}

// Three failed health probes take the instance out of traffic and one success brings it back
func TestHealthProbesSetReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	in, _, err := m.Run(ctx, soloRun(nil))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	n.launcher.SetHealthy("solo", false)
	waitFor(t, "unhealthy", func() bool {
		rec := current(t, m, in.GetId())
		return !rec.GetHealthy() && !n.registrar.Ready(in.GetId())
	})
	if c := ConditionOf(current(t, m, in.GetId()).GetConditions(), CondHealthy); c.GetReason() != ReasonProbeFailed {
		t.Fatalf("healthy condition %v", c)
	}
	n.launcher.SetHealthy("solo", true)
	waitFor(t, "healthy again", func() bool {
		return current(t, m, in.GetId()).GetHealthy() && n.registrar.Ready(in.GetId())
	})
	if current(t, m, in.GetId()).GetState() != v1.InstanceState_INSTANCE_STATE_READY {
		t.Fatal("an unhealthy spell does not restart the process")
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// A gRPC launch reserves two ports and publishes the gRPC port with metrics and health on the HTTP port
func TestGRPCEndpointPublished(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	in, _, err := m.Run(ctx, soloRun(map[string]string{fake.ParamGRPC: "true", fake.ParamMaxSeqs: "6"}))
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	port := portOf(t, ready.GetEndpoint())
	owner := ownerOf(in.GetId())
	eps := n.registrar.Endpoints(owner)
	if len(eps) != 1 || eps[0].Address.Port != port+1 || eps[0].MetricsPort != uint32(port) || eps[0].HealthPort != uint32(port) {
		t.Fatalf("published %+v for HTTP port %d", eps, port)
	}
	mb, _ := n.registrar.Membership(owner)
	if mb.Protocol != datalayer.AppProtocolGRPC || mb.Concurrency != 6 {
		t.Fatalf("membership %+v", mb)
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// A launch whose port another process took launches again on a fresh reservation, five tries an attempt
func TestPortTakenRelaunch(t *testing.T) {
	quickBackoff(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	in, task, err := m.Run(ctx, soloRun(map[string]string{fake.ParamPortTaken: "2", fake.ParamGRPC: "true"}))
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if got := n.launcher.Launches("solo"); got != 3 {
		t.Fatalf("%d launches, want two taken ports then one bound", got)
	}
	if ready.GetRestarts() != 0 {
		t.Fatalf("taken ports are not crashes: %d restarts", ready.GetRestarts())
	}
	final, err := m.Tasks.Wait(ctx, task.GetId())
	if err != nil || final.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("the first attempt rides out taken ports: %v %v", final.GetState(), err)
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}

	bounded, task, err := m.Run(ctx, &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, Name: "bounded", Params: map[string]string{fake.ParamPortTaken: "5", fake.ParamGRPC: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	final, err = m.Tasks.Wait(ctx, task.GetId())
	if err != nil || final.GetState() != v1.TaskState_TASK_STATE_FAILED || !strings.Contains(final.GetError(), "found its port taken on 5 reservations") {
		t.Fatalf("five taken ports fail the attempt: %v %q %v", final.GetState(), final.GetError(), err)
	}
	waitState(t, m, bounded.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if got := n.launcher.Launches("bounded"); got != maxPortTries+1 {
		t.Fatalf("%d launches, want %d tries then a fresh attempt", got, maxPortTries+1)
	}
	if _, err := m.Stop(ctx, bounded.GetId()); err != nil {
		t.Fatal(err)
	}
}

// An idle instance unloads after its window keeping its route, and a wake launches it again
func TestIdleUnloadAndWake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	run := soloRun(nil)
	run.IdleUnloadSeconds = 1
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	owner := ownerOf(in.GetId())
	idle := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_IDLE)
	if !idle.GetDesiredRunning() || len(n.registrar.Endpoints(owner)) != 0 || !slices.Contains(n.launcher.StopOrder(), "solo") {
		t.Fatalf("idle keeps intent, unpublishes, and stops: desired %v endpoints %v stops %v", idle.GetDesiredRunning(), n.registrar.Endpoints(owner), n.launcher.StopOrder())
	}
	if _, ok := n.registrar.Named("solo"); !ok {
		t.Fatal("an idle instance keeps its route for the wake")
	}
	if err := m.Wake(ctx, pools.OwnerRef{Kind: pools.OwnerInstance, ID: in.GetId()}); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if got := n.launcher.Launches("solo"); got != 2 || len(n.registrar.Endpoints(owner)) != 1 {
		t.Fatalf("the wake launched and published: %d launches, endpoints %v", got, n.registrar.Endpoints(owner))
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// Requests within the window keep an instance loaded
func TestIdleWindowCountsRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	run := soloRun(nil)
	run.IdleUnloadSeconds = 1
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	for range 6 {
		n.traffic.Dispatch(in.GetId(), 1)
		time.Sleep(300 * time.Millisecond)
	}
	if got := current(t, m, in.GetId()).GetState(); got != v1.InstanceState_INSTANCE_STATE_READY {
		t.Fatalf("a busy instance went %s", got)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_IDLE)
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// A runtime with sleep mode sleeps through its idle window and wakes in place without a relaunch
func TestSleepModeWakesInPlace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	run := soloRun(map[string]string{fake.ParamSleep: "true"})
	run.IdleUnloadSeconds = 1
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	owner := ownerOf(in.GetId())
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_IDLE)
	proc := n.launcher.Proc("solo")
	if !proc.Asleep() || proc.Sleeps.Load() != 1 || len(n.launcher.StopOrder()) != 0 || len(n.registrar.Endpoints(owner)) != 0 {
		t.Fatalf("asleep %v sleeps %d stops %v endpoints %v", proc.Asleep(), proc.Sleeps.Load(), n.launcher.StopOrder(), n.registrar.Endpoints(owner))
	}
	if err := m.Wake(ctx, pools.OwnerRef{Kind: pools.OwnerInstance, ID: in.GetId()}); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if proc.Asleep() || proc.Wakes.Load() != 1 || n.launcher.Launches("solo") != 1 || len(n.registrar.Endpoints(owner)) != 1 {
		t.Fatalf("asleep %v wakes %d launches %d endpoints %v", proc.Asleep(), proc.Wakes.Load(), n.launcher.Launches("solo"), n.registrar.Endpoints(owner))
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// Loading an adapter republishes the instance through the pool manager with an endpoint scoped to it, and unloading takes it away
func TestAdapterLoadRepublishes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	pm, err := pools.New(ctx, pools.Options{DB: n.db, Events: events.New(), NodeID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pm.Close() })
	m.Registrar = pm
	in, _, err := m.Run(ctx, soloRun(map[string]string{fake.ParamLoRA: "true"}))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if err := m.LoadAdapter(ctx, in.GetId(), "sql", "/adapters/sql"); err != nil {
		t.Fatal(err)
	}
	pool, adapter, err := pm.Lookup(ctx, "sql")
	if err != nil || adapter != "sql" {
		t.Fatalf("lookup of the loaded adapter %v %q %v", pool.ID, adapter, err)
	}
	eps := pm.Store().Endpoints(pool.ID, nil)
	if len(eps) != 2 || eps[0].Address() != eps[1].Address() {
		t.Fatalf("published %v", eps)
	}
	scoped, ok := pm.Store().Endpoint(datalayer.EndpointID(in.GetId() + "/" + strconv.Itoa(eps[0].Address().Port) + "/sql"))
	if !ok || scoped.Labels()[datalayer.LabelAdapter] != "sql" || !scoped.Ready() {
		t.Fatalf("adapter scoped endpoint %v %v", scoped, ok)
	}
	list, err := m.ListAdapters(ctx, in.GetId())
	if err != nil || len(list) != 1 || list[0].Name != "sql" || list[0].Path != "/adapters/sql" {
		t.Fatalf("adapters %v %v", list, err)
	}
	if err := m.UnloadAdapter(ctx, in.GetId(), "sql"); err != nil {
		t.Fatal(err)
	}
	if eps := pm.Store().Endpoints(pool.ID, nil); len(eps) != 1 || eps[0].Labels()[datalayer.LabelAdapter] != "" {
		t.Fatalf("after unload %v", eps)
	}
	plain, _, err := m.Run(ctx, &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, Name: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, plain.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if err := m.LoadAdapter(ctx, plain.GetId(), "sql", "/adapters/sql"); err == nil {
		t.Fatal("a launch without adapter support refuses a load")
	}
	m.Stop(ctx, plain.GetId())
	m.Stop(ctx, in.GetId())
}

// A request change bumps the generation and the reconciler rolls the process to it
func TestApplyRollsGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	in, _, err := m.Run(ctx, soloRun(nil))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	applied, err := m.Apply(ctx, in.GetId(), soloRun(map[string]string{fake.ParamMaxSeqs: "8"}))
	if err != nil {
		t.Fatal(err)
	}
	if applied.GetGeneration() != 2 || applied.GetObservedGeneration() != 1 {
		t.Fatalf("generation %d observed %d", applied.GetGeneration(), applied.GetObservedGeneration())
	}
	waitFor(t, "the new generation", func() bool {
		rec := current(t, m, in.GetId())
		return rec.GetObservedGeneration() == 2 && rec.GetState() == v1.InstanceState_INSTANCE_STATE_READY
	})
	if got := n.launcher.Launches("solo"); got != 2 {
		t.Fatalf("%d launches", got)
	}
	if mb, _ := n.registrar.Membership(ownerOf(in.GetId())); mb.Concurrency != 8 {
		t.Fatalf("membership %+v", mb)
	}
	if rec := current(t, m, in.GetId()); rec.GetRestarts() != 0 {
		t.Fatalf("a request change is not a crash: %d restarts", rec.GetRestarts())
	}
	m.Stop(ctx, in.GetId())
}

// A daemon closing stops instances through the drain keeping intent, and the next daemon relaunches them in place
func TestCloseThenRecoverRelaunchesInPlace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	first := n.manager(t, ctx, nil)
	in, _, err := first.Run(ctx, soloRun(nil))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, first, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	first.Close()
	closed := current(t, first, in.GetId())
	if closed.GetState() != v1.InstanceState_INSTANCE_STATE_STOPPED || !closed.GetDesiredRunning() || n.registrar.Drains(ownerOf(in.GetId())) != 1 {
		t.Fatalf("state %s desired %v drains %d", closed.GetState(), closed.GetDesiredRunning(), n.registrar.Drains(ownerOf(in.GetId())))
	}
	second := n.manager(t, ctx, nil)
	if err := second.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	again := waitState(t, second, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if again.GetId() != in.GetId() || n.launcher.Launches("solo") != 2 {
		t.Fatalf("relaunched as %s after %d launches", again.GetId(), n.launcher.Launches("solo"))
	}
	if _, ok := n.registrar.Named("solo"); !ok {
		t.Fatal("the relaunch names its route again")
	}
	second.Stop(ctx, in.GetId())
}

// A running process left by a previous daemon is adopted through its redacted command and keeps serving
func TestAdoptMatchesRedactedCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	first := n.manager(t, ctx, nil)
	in, _, err := first.Run(ctx, soloRun(nil))
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, first, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if !running(int(ready.GetPid()), ready.GetCommand()) {
		t.Fatal("the redacted command matches its own process")
	}
	second := n.manager(t, ctx, nil)
	if err := second.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	adopted := waitState(t, second, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if adopted.GetPid() != ready.GetPid() || n.launcher.Launches("solo") != 1 {
		t.Fatalf("pid %d then %d after %d launches", ready.GetPid(), adopted.GetPid(), n.launcher.Launches("solo"))
	}
	if len(n.registrar.Endpoints(ownerOf(in.GetId()))) != 1 {
		t.Fatal("the adopted instance publishes")
	}
	second.Stop(ctx, in.GetId())
}

// The gateway reads tool support, modes and the chat template of a ready instance and nothing of an unknown one
func TestGatewayReaders(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	in, _, err := m.Run(ctx, soloRun(nil))
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if got := m.ToolSupport(in.GetId()); got != ready.GetTemplate().GetTools() {
		t.Fatalf("tool support %v, record %v", got, ready.GetTemplate())
	}
	if modes := m.Modes(in.GetId()); len(modes) != 0 {
		t.Fatalf("a language instance reports modes %v", modes)
	}
	tpl, err := m.Template(in.GetId())
	switch {
	case ready.GetTemplate().GetError() != "" && err == nil:
		t.Fatalf("the record holds no template (%s) yet one was read", ready.GetTemplate().GetError())
	case ready.GetTemplate().GetError() == "" && (err != nil || tpl.Source != ready.GetTemplate().GetSource()):
		t.Fatalf("template %v %v, record %v", tpl, err, ready.GetTemplate())
	}
	if m.ToolSupport("nope") != v1.ToolSupport_TOOL_SUPPORT_UNSPECIFIED || m.Modes("nope") != nil {
		t.Fatal("an unknown instance has no readings")
	}
	if _, err := m.Template("nope"); !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("unknown instance template %v", err)
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// A change that panics leaves the record lock free, so the manager keeps answering after the task runner recovers it
func TestUpdateReleasesTheLockOnPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	in := m.newInstance(&v1.Instance{Id: db.NewID(), Name: "solo", State: v1.InstanceState_INSTANCE_STATE_STARTING}, fake.Runtime{})
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic did not surface")
			}
		}()
		in.update(func(*v1.Instance) { panic("boom") })
	}()
	got := make(chan string, 1)
	go func() { got <- in.snapshot().GetName() }()
	select {
	case name := <-got:
		if name != "solo" {
			t.Fatalf("name %q", name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the record lock stayed held after a panic")
	}
}
