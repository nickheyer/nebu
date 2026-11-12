package instances

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/nickheyer/nebu/internal/instances/fake"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

// A slot manager holding one reservation and the runs handed to it to settle on
type oneSlot struct {
	res     *Reservation
	settled *[]*v1.RunRequest
}

func (s oneSlot) Reservation(context.Context, string) (*Reservation, error) { return s.res, nil }
func (oneSlot) OnInstance(*v1.Instance)                                     {}
func (s oneSlot) Owns(name string) bool                                     { return name == s.res.Name }
func (s oneSlot) Settle(_ string, run *v1.RunRequest) error {
	*s.settled = append(*s.settled, run)
	return nil
}

// A run's words replace the slot's, the slot is handed them, the request carries every setting, bad words are refused
func TestRunArgs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	var settled []*v1.RunRequest
	m.Slots = oneSlot{&Reservation{SlotID: "s", Name: "main", Args: "--cache-reuse 256", Params: map[string]string{fake.ParamMaxSeqs: "2"}}, &settled}
	run := &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, SlotId: "s", Args: `--cache-reuse 512 --alias "{{.Name}} run"`}

	bad := &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, Args: `--alias "open`}
	if _, _, err := m.Run(ctx, bad); !errors.Is(err, runtimes.ErrParam) {
		t.Fatalf("unclosed quote: %v", err)
	}

	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if req := in.GetRequest(); req.GetArgs() != run.GetArgs() || !req.GetExact() || req.GetParams()[fake.ParamMaxSeqs] != "2" {
		t.Fatalf("the request carries every setting it launched with: %v", req)
	}
	if len(settled) != 1 || settled[0].GetArgs() != run.GetArgs() || settled[0].GetExact() || len(settled[0].GetParams()) != 0 {
		t.Fatalf("the slot is handed the run as it was asked: %v", settled)
	}
	cmd := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY).GetCommand()
	want := []string{"--cache-reuse", "512", "--alias", "main run"}
	if len(cmd) < len(want) || !slices.Equal(cmd[len(cmd)-len(want):], want) || slices.Contains(cmd, "256") {
		t.Fatalf("command %q ends in %q", cmd, want)
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
	// A run naming no words launches with its slot's, an exact run with none of the slot's settings, a swap's replacement settling nothing
	for _, c := range []struct {
		ctx     context.Context
		run     *v1.RunRequest
		want    []string
		settles int
	}{
		{ctx, &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, SlotId: "s"}, []string{"--cache-reuse", "256"}, 2},
		{ctx, &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, SlotId: "s", Exact: true, Args: "--exact 1"}, []string{"--exact", "1"}, 3},
		{Unsettled(ctx), &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, SlotId: "s", Args: "--swap 1"}, []string{"--swap", "1"}, 3},
	} {
		next, _, err := m.Run(c.ctx, c.run)
		if err != nil {
			t.Fatal(err)
		}
		got := waitState(t, m, next.GetId(), v1.InstanceState_INSTANCE_STATE_READY).GetCommand()
		if len(got) < len(c.want) || !slices.Equal(got[len(got)-len(c.want):], c.want) || len(settled) != c.settles {
			t.Fatalf("run %v: command %q ends in %q, settled %d times, want %d", c.run, got, c.want, len(settled), c.settles)
		}
		if c.run.GetExact() && next.GetRequest().GetParams()[fake.ParamMaxSeqs] != "" {
			t.Fatalf("an exact run takes no param of its slot: %v", next.GetRequest())
		}
		if _, err := m.Stop(ctx, next.GetId()); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := n.db.ListInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := slices.IndexFunc(saved, func(r *v1.Instance) bool { return r.GetId() == in.GetId() })
	if found < 0 || saved[found].GetRequest().GetArgs() != run.GetArgs() {
		t.Fatalf("saved request lost its args: %v", saved)
	}
}

// An occupant recorded before requests carried every setting keeps the words it launched with, the slot's then its own
func TestInheritKeepsOlderWords(t *testing.T) {
	for _, c := range []struct{ slot, run, want string }{
		{"--slot 1 --run 1", "--run 1", "--slot 1 --run 1"},
		{"--slot 1", "--slot 1", "--slot 1"},
		{"--slot 1", "", "--slot 1"},
		{"--run 1", "--run 1", "--run 1"},
		{"--edited 1", "--run 1", "--run 1"},
	} {
		p := &prepared{req: &v1.RunRequest{Args: c.run, Params: map[string]string{"a": "run"}}, res: &Reservation{Args: c.slot, Params: map[string]string{"a": "slot", "b": "slot"}}}
		p.inherit()
		if p.req.GetArgs() != c.want || !p.req.GetExact() || p.req.GetParams()["a"] != "run" || p.req.GetParams()["b"] != "slot" {
			t.Errorf("slot %q run %q: %v, want words %q", c.slot, c.run, p.req, c.want)
		}
	}
	exact := &prepared{req: &v1.RunRequest{Exact: true, Args: "--run 1"}, res: &Reservation{Args: "--slot 1 --run 1"}}
	exact.inherit()
	if exact.req.GetArgs() != "--run 1" || len(exact.req.GetParams()) != 0 {
		t.Fatalf("an exact request is left as it is: %v", exact.req)
	}
}

// A seat renders the extra words its block carries, and a helper seat leaves them out
func TestSeatArgs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	want := []string{"--cache-reuse", "256", "--cache-reuse", "512"}

	helper := seatRun(runtimes.RoleStage, 1, false, n.dir)
	helper.Seat.Args = "--cache-reuse 256 --cache-reuse 512"
	stage, _, err := m.Run(ctx, helper)
	if err != nil {
		t.Fatal(err)
	}
	stage = waitState(t, m, stage.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if slices.Contains(stage.GetCommand(), "--cache-reuse") {
		t.Fatalf("helper command %q", stage.GetCommand())
	}
	if _, err := m.Stop(ctx, stage.GetId()); err != nil {
		t.Fatal(err)
	}

	run := seatRun(runtimes.RoleHead, 0, false, n.dir)
	run.Seat.Args = "--cache-reuse 256 --cache-reuse 512"
	head, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	cmd := waitState(t, m, head.GetId(), v1.InstanceState_INSTANCE_STATE_READY).GetCommand()
	if len(cmd) < len(want) || !slices.Equal(cmd[len(cmd)-len(want):], want) {
		t.Fatalf("command %q ends in %q", cmd, want)
	}
	if _, err := m.Stop(ctx, head.GetId()); err != nil {
		t.Fatal(err)
	}
}
