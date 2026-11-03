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

// A slot manager holding one reservation
type oneSlot struct{ res *Reservation }

func (s oneSlot) Reservation(context.Context, string) (*Reservation, error) { return s.res, nil }
func (oneSlot) OnInstance(*v1.Instance)                                     {}
func (s oneSlot) Owns(name string) bool                                     { return name == s.res.Name }

// A run's words follow the slot's in the command, persist on its request, and bad words are refused
func TestRunArgs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	m.Slots = oneSlot{&Reservation{SlotID: "s", Name: "main", Args: "--cache-reuse 256"}}
	run := &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, SlotId: "s", Args: `--cache-reuse 512 --alias "{{.Name}} run"`}

	bad := &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, Args: `--alias "open`}
	if _, _, err := m.Run(ctx, bad); !errors.Is(err, runtimes.ErrParam) {
		t.Fatalf("unclosed quote: %v", err)
	}

	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if in.GetRequest().GetArgs() != run.GetArgs() {
		t.Fatalf("request args %q", in.GetRequest().GetArgs())
	}
	cmd := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY).GetCommand()
	want := []string{"--cache-reuse", "256", "--cache-reuse", "512", "--alias", "main run"}
	if len(cmd) < len(want) || !slices.Equal(cmd[len(cmd)-len(want):], want) {
		t.Fatalf("command %q ends in %q", cmd, want)
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
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
