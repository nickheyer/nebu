package slots

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/nickheyer/nebu/internal/instances/fake"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Whether the slot holds exactly these launch settings and a request carrying none of its own
func settled(s *v1.Slot, runtime, install, args string, params map[string]string) bool {
	r := s.GetRequest()
	bare := r.GetRuntimeId() == "" && r.GetInstallId() == "" && r.GetArgs() == "" && len(r.GetParams()) == 0 && !r.GetExact()
	return bare && s.GetRuntimeId() == runtime && s.GetInstallId() == install && s.GetArgs() == args && maps.Equal(s.GetParams(), params)
}

// A slot takes the settings of every run into it, keeps them through an eviction, and launches its next occupant with what its editor saved
func TestSlotKeepsRunSettings(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSlotNode(t, ctx)
	s, err := n.slots.Create(ctx, &v1.CreateSlotRequest{Name: "main", Params: map[string]string{fake.ParamMaxSeqs: "2"}, Args: "--slot 1"})
	if err != nil {
		t.Fatal(err)
	}
	ready := func(what string) *v1.Slot {
		t.Helper()
		waitFor(t, what, func() bool { return n.slot(t, s.GetId()).GetState() == v1.SlotState_SLOT_STATE_READY })
		return n.slot(t, s.GetId())
	}

	// A run laid over the slot's settings becomes the slot's settings, its install left to the runtime's newest
	run := slotRun(s.GetId(), "q4")
	run.Params, run.Args = map[string]string{fake.ParamAlias: "first"}, "--run 1"
	if _, _, err := n.instances.Run(ctx, run); err != nil {
		t.Fatal(err)
	}
	merged := map[string]string{fake.ParamMaxSeqs: "2", fake.ParamAlias: "first"}
	if got := ready("the run to serve"); !settled(got, fake.RuntimeID, "", "--run 1", merged) || got.GetRequest().GetGroup() != "q4" {
		t.Fatalf("slot after a run: %v", got)
	}
	occ := n.occupant(t, s.GetId())
	if req := occ.GetRequest(); !req.GetExact() || req.GetArgs() != "--run 1" || !maps.Equal(req.GetParams(), merged) {
		t.Fatalf("the occupant's request carries every setting it launched with: %v", req)
	}
	saved, err := n.db.ListSlots(ctx)
	if err != nil || len(saved) != 1 || !settled(saved[0], fake.RuntimeID, "", "--run 1", merged) {
		t.Fatalf("saved %v %v", saved, err)
	}

	// Eviction clears the model and keeps the settings
	if _, err := n.slots.Evict(ctx, s.GetId()); err != nil {
		t.Fatal(err)
	}
	if got := n.slot(t, s.GetId()); got.GetRequest() != nil || got.GetRuntimeId() != fake.RuntimeID || got.GetArgs() != "--run 1" || !maps.Equal(got.GetParams(), merged) {
		t.Fatalf("slot after eviction: %v", got)
	}

	// An exact run replaces every setting, and pins the install it names
	install := occ.GetInstallId()
	exact := slotRun(s.GetId(), "q8")
	exact.Exact, exact.InstallId, exact.Params = true, install, map[string]string{fake.ParamAlias: "second"}
	if _, _, _, err := n.slots.Swap(ctx, &v1.SwapRequest{SlotId: s.GetId(), Run: exact}); err != nil {
		t.Fatal(err)
	}
	if got := ready("the exact run to serve"); !settled(got, fake.RuntimeID, install, "", map[string]string{fake.ParamAlias: "second"}) || got.GetRequest().GetGroup() != "q8" {
		t.Fatalf("slot after an exact run: %v", got)
	}
	if words := n.occupant(t, s.GetId()).GetCommand(); len(words) == 0 || words[len(words)-1] == "1" {
		t.Fatalf("an exact run naming no words launches with none: %q", words)
	}

	// Settings saved while it serves reach the next occupant, a swap naming only its model
	edited := map[string]string{fake.ParamAlias: "third", fake.ParamMaxSeqs: "4"}
	if _, err := n.slots.Update(ctx, &v1.UpdateSlotRequest{Id: s.GetId(), RuntimeId: fake.RuntimeID, InstallId: install, Params: edited, Args: "--edited 1"}); err != nil {
		t.Fatal(err)
	}
	before := n.occupant(t, s.GetId())
	task := func() *v1.Task {
		_, _, task, err := n.slots.Swap(ctx, &v1.SwapRequest{SlotId: s.GetId(), Run: &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4"}})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}()
	if err := n.instances.Tasks.WaitOK(ctx, task.GetId()); err != nil {
		t.Fatal(err)
	}
	got := ready("the edited slot to serve")
	after := n.occupant(t, s.GetId())
	if after.GetId() == before.GetId() || !settled(got, fake.RuntimeID, install, "--edited 1", edited) {
		t.Fatalf("slot after its settings were edited: %v", got)
	}
	if req := after.GetRequest(); !maps.Equal(req.GetParams(), edited) || req.GetArgs() != "--edited 1" || after.GetInstallId() != install {
		t.Fatalf("the next occupant launches with the saved settings: %v", after)
	}

	// An install the slot cannot run is refused
	if _, err := n.slots.Update(ctx, &v1.UpdateSlotRequest{Id: s.GetId(), RuntimeId: fake.RuntimeID, InstallId: "nope"}); !errors.Is(err, ErrSlot) {
		t.Fatalf("an unknown install: %v", err)
	}
	if _, err := n.slots.Update(ctx, &v1.UpdateSlotRequest{Id: s.GetId(), RuntimeId: "other", InstallId: install}); !errors.Is(err, ErrSlot) {
		t.Fatalf("an install of another runtime: %v", err)
	}
	if got, err := n.slots.Update(ctx, &v1.UpdateSlotRequest{Id: s.GetId(), InstallId: install}); err != nil || got.GetRuntimeId() != fake.RuntimeID {
		t.Fatalf("an install alone names its runtime: %v %v", got, err)
	}
}

// A replacement that never serves leaves the slot's settings as its occupant launched with them
func TestSwapSettlesOnlyOnAServingReplacement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSlotNode(t, ctx)
	s := n.filled(t, ctx)
	kept := map[string]string{fake.ParamAlias: "kept"}
	if _, err := n.slots.Update(ctx, &v1.UpdateSlotRequest{Id: s.GetId(), Description: s.GetDescription(), Aliases: s.GetAliases(), RuntimeId: fake.RuntimeID, Params: kept, Args: "--kept 1"}); err != nil {
		t.Fatal(err)
	}
	old := n.occupant(t, s.GetId())
	for _, drain := range []bool{false, true} {
		bad := slotRun(s.GetId(), "q8")
		bad.Params, bad.Args = map[string]string{fake.ParamFail: "true"}, "--bad 1"
		_, _, task, err := n.slots.Swap(ctx, &v1.SwapRequest{SlotId: s.GetId(), Run: bad, DrainFirst: drain})
		if err != nil {
			t.Fatal(err)
		}
		if err := n.instances.Tasks.WaitOK(ctx, task.GetId()); err == nil {
			t.Fatalf("drain first %v: a replacement that fails at start ends its swap in failure", drain)
		}
		waitFor(t, "the slot to settle", func() bool { return n.slot(t, s.GetId()).GetState() == v1.SlotState_SLOT_STATE_READY })
		got := n.slot(t, s.GetId())
		if !settled(got, fake.RuntimeID, "", "--kept 1", kept) || got.GetRequest().GetGroup() != "q4" {
			t.Fatalf("drain first %v: slot after a failed swap: %v", drain, got)
		}
		serving := n.occupant(t, s.GetId())
		if drain && (serving.GetId() == old.GetId() || serving.GetRequest().GetParams()[fake.ParamFail] != "" || serving.GetRequest().GetArgs() != "--kept 1") {
			t.Fatalf("the rollback launches the previous model with the slot's settings: %v", serving)
		}
		if !drain && serving.GetId() != old.GetId() {
			t.Fatalf("the occupant serves on through a failed replacement: %v", serving)
		}
	}
}

// A slot stored before slots kept their settings takes them from its request when loaded
func TestLoadMovesRequestSettingsOntoTheSlot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSlotNode(t, ctx)
	now := timestamppb.Now()
	older := &v1.Slot{
		Id: "old", Name: "older", State: v1.SlotState_SLOT_STATE_FAILED, CreatedAt: now, UpdatedAt: now, Meta: &v1.ObjectMeta{Generation: 3},
		Params: map[string]string{fake.ParamMaxSeqs: "2"}, Args: "--slot 1",
		Request: &v1.RunRequest{SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, InstallId: "pinned", Params: map[string]string{fake.ParamAlias: "a"}, Args: "--run 1", SlotId: "old"},
	}
	if err := n.db.PutSlot(ctx, older); err != nil {
		t.Fatal(err)
	}
	if err := n.slots.Load(ctx); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{fake.ParamMaxSeqs: "2", fake.ParamAlias: "a"}
	if got := n.slot(t, "old"); !settled(got, fake.RuntimeID, "pinned", "--slot 1 --run 1", want) || got.GetRequest().GetGroup() != "q4" || got.GetMeta().GetGeneration() != 4 {
		t.Fatalf("loaded %v", got)
	}
	saved, err := n.db.ListSlots(ctx)
	if err != nil || len(saved) != 1 || !settled(saved[0], fake.RuntimeID, "pinned", "--slot 1 --run 1", want) {
		t.Fatalf("saved %v %v", saved, err)
	}
	if err := n.slots.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := n.slot(t, "old"); got.GetMeta().GetGeneration() != 4 || got.GetArgs() != "--slot 1 --run 1" {
		t.Fatalf("a second load changes nothing: %v", got)
	}
}
