package slots

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/pools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// An alias takes the current limits and tools of the slot it inherits from and loses them with it
func TestAliasInheritsAnotherSlot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSlotNode(t, ctx)
	base, err := n.slots.Create(ctx, &v1.CreateSlotRequest{Name: "base", Policy: &v1.Policy{MaxInFlight: 2, Burst: 4}, Profile: &v1.Profile{ToolServers: []string{"web"}}})
	if err != nil {
		t.Fatal(err)
	}
	s := n.filled(t, ctx)
	own := &v1.Policy{MaxInFlight: 9}
	next, err := n.slots.Update(ctx, &v1.UpdateSlotRequest{Id: s.GetId(), Policy: own, Aliases: []*v1.SlotAlias{{Name: "chat", InheritSlotId: " base "}, {Name: "plain"}}})
	if err != nil {
		t.Fatal(err)
	}
	if a := next.GetAliases(); len(a) != 2 || a[0].GetInheritSlotId() != base.GetId() || a[1].GetInheritSlotId() != "" {
		t.Fatalf("an inherit is stored by the slot's id %v", a)
	}
	wantSettings := func(name string, policy *v1.Policy, profile *v1.Profile) {
		t.Helper()
		rt, ok := n.registrar.Named(name)
		if !ok || !proto.Equal(rt.Spec.Policy, policy) || !proto.Equal(rt.Spec.Profile, profile) {
			t.Fatalf("route %s policy %v profile %v, want %v %v", name, rt.Spec.Policy, rt.Spec.Profile, policy, profile)
		}
	}
	wantSettings("chat", base.GetPolicy(), base.GetProfile())
	wantSettings("plain", own, nil)
	wantSettings("main", own, nil)

	changed := &v1.Policy{MaxInFlight: 3}
	tools := &v1.Profile{ToolServers: []string{"fs"}}
	if _, err := n.slots.Update(ctx, &v1.UpdateSlotRequest{Id: base.GetId(), Policy: changed, Profile: tools}); err != nil {
		t.Fatal(err)
	}
	wantSettings("chat", changed, tools)
	wantSettings("main", own, nil)

	refusals := map[string]*v1.SlotAlias{
		"its own slot by name": {Name: "loop", InheritSlotId: "main"},
		"its own slot by id":   {Name: "loop", InheritSlotId: s.GetId()},
		"a slot not there":     {Name: "lost", InheritSlotId: "nope"},
	}
	for what, alias := range refusals {
		if _, err := n.slots.Update(ctx, &v1.UpdateSlotRequest{Id: s.GetId(), Policy: own, Aliases: []*v1.SlotAlias{alias}}); !errors.Is(err, ErrSlot) {
			t.Fatalf("an alias inheriting %s: %v", what, err)
		}
	}
	if _, err := n.slots.Create(ctx, &v1.CreateSlotRequest{Name: "fresh", Aliases: []*v1.SlotAlias{{Name: "again", InheritSlotId: "fresh"}}}); !errors.Is(err, ErrSlot) {
		t.Fatalf("a new slot's alias inheriting the slot itself: %v", err)
	}

	gen := n.slot(t, s.GetId()).GetMeta().GetGeneration()
	sub := n.slots.Events.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_SLOT})
	if _, err := n.slots.Delete(ctx, base.GetId(), false); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for published := false; !published; {
		select {
		case ev := <-sub.Events():
			a := ev.GetSlot().GetAliases()
			published = ev.GetId() == s.GetId() && ev.GetAction() == v1.EventAction_EVENT_ACTION_UPDATED && len(a) == 2 && a[0].GetInheritSlotId() == "" && ev.GetSlot().GetMeta().GetGeneration() == gen+1
		case <-deadline:
			t.Fatal("no slot event carried the cleared inherit")
		}
	}
	cur := n.slot(t, s.GetId())
	if cur.GetAliases()[0].GetInheritSlotId() != "" || cur.GetMeta().GetGeneration() != gen+1 {
		t.Fatalf("after the delete aliases %v generation %d, want %d", cur.GetAliases(), cur.GetMeta().GetGeneration(), gen+1)
	}
	saved, err := n.db.ListSlots(ctx)
	if err != nil || len(saved) != 1 || saved[0].GetAliases()[0].GetInheritSlotId() != "" || saved[0].GetMeta().GetGeneration() != gen+1 {
		t.Fatalf("saved after the delete %v %v", saved, err)
	}
	wantSettings("chat", own, nil)
}

// A user policy over a slot alias route outlives a restart through the real pool manager, adopted or relaunched
func TestRecoverKeepsAliasRoutePolicy(t *testing.T) {
	cases := []struct {
		name string
		// Whether the occupant's record is gone at the restart, so the slot launches another
		lost bool
	}{
		{"occupant adopted", false},
		{"occupant relaunched", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			n := newSlotNode(t, ctx)
			pm := n.realPools(t, ctx)
			s := n.filled(t, ctx)
			r, ok := pm.Route("chat")
			if !ok || r.GetOwnerKind() != pools.OwnerSlot || r.GetOwnerId() != s.GetId() {
				t.Fatalf("alias route %v %v", r, ok)
			}
			if _, err := pm.PutRoute(ctx, "chat", &v1.RouteSpec{Rules: r.GetSpec().GetRules(), Policy: &v1.Policy{MaxInFlight: 9}}); err != nil {
				t.Fatal(err)
			}
			first := n.occupant(t, s.GetId())
			n.instances.Close()
			if c.lost {
				if err := n.db.DeleteInstance(ctx, first.GetId()); err != nil {
					t.Fatal(err)
				}
			}
			if err := pm.Close(); err != nil {
				t.Fatal(err)
			}
			n.start(t, ctx)
			pm = n.realPools(t, ctx)
			if err := n.instances.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			if err := n.slots.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the slot serving again", func() bool {
				cur := n.slot(t, s.GetId())
				return cur.GetInstanceId() != "" && (cur.GetInstanceId() != first.GetId()) == c.lost && cur.GetState() == v1.SlotState_SLOT_STATE_READY
			})
			r, ok = pm.Route("chat")
			if !ok || r.GetOwnerKind() != pools.OwnerSlot || r.GetSpec().GetPolicy().GetMaxInFlight() != 9 {
				t.Fatalf("alias route after the restart %v %v", r, ok)
			}
			if rows, err := n.db.ListRouteOverrides(ctx); err != nil || len(rows) != 1 || rows[0].GetName() != "chat" {
				t.Fatalf("override rows after the restart %v %v", rows, err)
			}
			if r, _ := pm.Route("main"); r.GetSpec().GetPolicy().GetMaxInFlight() != 0 {
				t.Fatalf("the slot's own route took the alias override %v", r.GetSpec())
			}
		})
	}
}

// Loading a slot whose alias inherits from a slot no longer stored clears and saves that inherit
func TestLoadClearsInheritOfGoneSlot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSlotNode(t, ctx)
	s, err := n.slots.Create(ctx, &v1.CreateSlotRequest{Name: "main", Aliases: []*v1.SlotAlias{{Name: "chat"}}})
	if err != nil {
		t.Fatal(err)
	}
	stale := proto.Clone(s).(*v1.Slot)
	stale.Aliases[0].InheritSlotId = "gone"
	if err := n.db.PutSlot(ctx, stale); err != nil {
		t.Fatal(err)
	}
	n.instances.Close()
	n.start(t, ctx)
	cur := n.slot(t, s.GetId())
	if cur.GetAliases()[0].GetInheritSlotId() != "" || cur.GetMeta().GetGeneration() != s.GetMeta().GetGeneration()+1 {
		t.Fatalf("loaded aliases %v generation %d", cur.GetAliases(), cur.GetMeta().GetGeneration())
	}
	if saved, err := n.db.ListSlots(ctx); err != nil || len(saved) != 1 || saved[0].GetAliases()[0].GetInheritSlotId() != "" {
		t.Fatalf("saved %v %v", saved, err)
	}
}
