package instances

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/internal/pools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The argument after a flag in a fake process command line
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// A one port launch binds port 0 and the manager reads the port back from the process, while a two port launch keeps a reservation
func TestEphemeralPortReadBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	in, _, err := m.Run(ctx, soloRun(nil))
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	proc := n.launcher.Proc("solo")
	if got := flagValue(proc.Args, "--port"); got != "0" {
		t.Fatalf("a one port launch asks the kernel for its port, got --port %s", got)
	}
	if port := portOf(t, ready.GetEndpoint()); port == 0 || port != proc.Port() {
		t.Fatalf("endpoint %s, the process listens on %d", ready.GetEndpoint(), proc.Port())
	}
	if eps := n.registrar.Endpoints(ownerOf(in.GetId())); len(eps) != 1 || eps[0].Address.Port != proc.Port() {
		t.Fatalf("published %+v for port %d", eps, proc.Port())
	}
	if inv := ready.GetInventory(); inv.GetAccelerator() != "cpu" || strings.Join(inv.GetModalities(), ",") != "text" || inv.GetThroughputClass() != "host" {
		t.Fatalf("inventory %+v", inv)
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
	grpc, _, err := m.Run(ctx, &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, Name: "pair", Params: map[string]string{fake.ParamGRPC: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	ready = waitState(t, m, grpc.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	pair := n.launcher.Proc("pair")
	if got := flagValue(pair.Args, "--port"); got == "0" || got == "" {
		t.Fatalf("a two port launch runs on a reservation, got --port %q", got)
	}
	if port := portOf(t, ready.GetEndpoint()); port != pair.Port() {
		t.Fatalf("endpoint %s, the process listens on %d", ready.GetEndpoint(), pair.Port())
	}
	if _, err := m.Stop(ctx, grpc.GetId()); err != nil {
		t.Fatal(err)
	}
}

// A runtime whose sleep call fails is unloaded by stopping, the failure recorded, and a wake launches it again
func TestSleepFailureUnloadsByStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	run := soloRun(map[string]string{fake.ParamSleep: "true", fake.ParamFailSleep: "true"})
	run.IdleUnloadSeconds = 1
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	owner := ownerOf(in.GetId())
	idle := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_IDLE)
	proc := n.launcher.Proc("solo")
	if proc.Sleeps.Load() != 1 || proc.Asleep() {
		t.Fatalf("sleeps %d asleep %v", proc.Sleeps.Load(), proc.Asleep())
	}
	waitFor(t, "the process stopped", func() bool { return len(n.launcher.StopOrder()) == 1 })
	if len(n.registrar.Endpoints(owner)) != 0 || !strings.Contains(idle.GetError(), "sleep failed, unloaded by stopping") {
		t.Fatalf("endpoints %v error %q", n.registrar.Endpoints(owner), idle.GetError())
	}
	if c := ConditionOf(idle.GetConditions(), CondReady); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE || c.GetReason() != ReasonIdle || !strings.Contains(c.GetMessage(), "refused to sleep") {
		t.Fatalf("ready condition %v", c)
	}
	if err := m.Wake(ctx, pools.OwnerRef{Kind: pools.OwnerInstance, ID: in.GetId()}); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if got := n.launcher.Launches("solo"); got != 2 || len(n.registrar.Endpoints(owner)) != 1 {
		t.Fatalf("the wake launched again: %d launches, endpoints %v", got, n.registrar.Endpoints(owner))
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// Scraped running or queued requests keep an instance loaded through its idle window as the dispatch counter does
func TestIdleWindowCountsScrapedLoad(t *testing.T) {
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
	for _, load := range [][2]int{{1, 0}, {0, 2}} {
		n.traffic.SetLoad(in.GetId(), load[0], load[1])
		time.Sleep(2 * time.Second)
		if got := current(t, m, in.GetId()).GetState(); got != v1.InstanceState_INSTANCE_STATE_READY {
			t.Fatalf("an instance scraped at running %d queued %d went %s", load[0], load[1], got)
		}
	}
	n.traffic.SetLoad(in.GetId(), 0, 0)
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_IDLE)
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
	m.Load = nil
	if _, _, err := m.Run(ctx, run); err == nil || !strings.Contains(err.Error(), "scraped load") {
		t.Fatalf("idle unload without a scrape reader is refused: %v", err)
	}
}
