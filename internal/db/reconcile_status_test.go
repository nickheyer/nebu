package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Conditions as a reconciler writes them, with a transition time and the generation observed
func statusConditions(now time.Time) []*v1.Condition {
	return []*v1.Condition{
		{Type: "Ready", Status: v1.ConditionStatus_CONDITION_STATUS_TRUE, Reason: "Healthy", Message: "answered its probe", LastTransitionTime: timestamppb.New(now), ObservedGeneration: 3},
		{Type: "Draining", Status: v1.ConditionStatus_CONDITION_STATUS_FALSE, Reason: "Serving", LastTransitionTime: timestamppb.New(now)},
	}
}

// A request carrying route limits, route shaping and an idle window
func statusRequest(slotID string) *v1.RunRequest {
	return &v1.RunRequest{
		SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: "fake", InstallId: "i1", Name: "main", SlotId: slotID,
		Policy:            &v1.Policy{MaxInFlight: 4, RequestsPerSecond: 2.5, Burst: 8, RequestTimeoutMs: 1000, UpstreamTimeoutMs: 500},
		RouteProfile:      &v1.Profile{ToolServers: []string{"fs"}, MutatingTools: true},
		IdleUnloadSeconds: 300,
	}
}

// Status, desired state and request fields written by the reconcilers come back from the store unchanged
func TestReconcileStatusRoundTrip(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if len(d.Drift) != 0 {
		t.Fatalf("drift after migrating: %v", d.Drift)
	}
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	cases := []struct {
		name  string
		check func(t *testing.T, full bool)
	}{
		{"instance", func(t *testing.T, full bool) {
			in := &v1.Instance{Id: "in-" + word(full), Name: "main", State: v1.InstanceState_INSTANCE_STATE_READY, CreatedAt: timestamppb.New(now), Request: &v1.RunRequest{Repo: "org/model"}}
			if full {
				in.Conditions, in.Generation, in.ObservedGeneration, in.Restarts = statusConditions(now), 4, 3, 2
				in.LastHealthAt, in.Healthy, in.Request = timestamppb.New(now), true, statusRequest("")
			}
			if err := d.PutInstance(ctx, in); err != nil {
				t.Fatal(err)
			}
			list, err := d.ListInstances(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var got *v1.Instance
			for _, rec := range list {
				if rec.GetId() == in.GetId() {
					got = rec
				}
			}
			if got == nil {
				t.Fatalf("instance %s not listed", in.GetId())
			}
			if len(got.GetConditions()) != len(in.GetConditions()) || got.GetGeneration() != in.GetGeneration() || got.GetObservedGeneration() != in.GetObservedGeneration() || got.GetRestarts() != in.GetRestarts() || got.GetHealthy() != in.GetHealthy() {
				t.Fatalf("status %v, want %v", got, in)
			}
			for i, c := range in.GetConditions() {
				if !proto.Equal(c, got.GetConditions()[i]) {
					t.Fatalf("condition %d %v, want %v", i, got.GetConditions()[i], c)
				}
			}
			if !proto.Equal(got.GetLastHealthAt(), in.GetLastHealthAt()) {
				t.Fatalf("last health %v, want %v", got.GetLastHealthAt(), in.GetLastHealthAt())
			}
			sameRequestStatus(t, got.GetRequest(), in.GetRequest())
		}},
		{"slot", func(t *testing.T, full bool) {
			s := &v1.Slot{Id: "s-" + word(full), Name: "slot-" + word(full), State: v1.SlotState_SLOT_STATE_EMPTY, CreatedAt: timestamppb.New(now), UpdatedAt: timestamppb.New(now)}
			if full {
				s.Description, s.DesiredRunning, s.Conditions, s.ObservedGeneration = "the chat slot", true, statusConditions(now), 5
				s.Meta = &v1.ObjectMeta{Generation: 6, Labels: map[string]string{"team": "a"}, Annotations: map[string]string{"note": "b"}}
				s.Request, s.State = statusRequest(s.GetId()), v1.SlotState_SLOT_STATE_READY
			}
			if err := d.PutSlot(ctx, s); err != nil {
				t.Fatal(err)
			}
			list, err := d.ListSlots(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var got *v1.Slot
			for _, rec := range list {
				if rec.GetId() == s.GetId() {
					got = rec
				}
			}
			if got == nil {
				t.Fatalf("slot %s not listed", s.GetId())
			}
			if got.GetDescription() != s.GetDescription() || got.GetDesiredRunning() != s.GetDesiredRunning() || got.GetObservedGeneration() != s.GetObservedGeneration() || !proto.Equal(got.GetMeta(), s.GetMeta()) {
				t.Fatalf("slot %v, want %v", got, s)
			}
			if len(got.GetConditions()) != len(s.GetConditions()) {
				t.Fatalf("conditions %v, want %v", got.GetConditions(), s.GetConditions())
			}
			for i, c := range s.GetConditions() {
				if !proto.Equal(c, got.GetConditions()[i]) {
					t.Fatalf("condition %d %v, want %v", i, got.GetConditions()[i], c)
				}
			}
			sameRequestStatus(t, got.GetRequest(), s.GetRequest())
		}},
		{"formation", func(t *testing.T, full bool) {
			f := &v1.Formation{Id: "f-" + word(full), Name: "chain", Conductor: "n1", State: v1.FormationState_FORMATION_STATE_READY, CreatedAt: timestamppb.New(now), UpdatedAt: timestamppb.New(now), Request: &v1.RunRequest{Repo: "org/model"}}
			if full {
				f.Generation, f.ObservedGeneration, f.Conditions, f.Relaunches = 7, 6, statusConditions(now), 2
				f.Request = statusRequest("")
			}
			if err := d.PutFormation(ctx, f); err != nil {
				t.Fatal(err)
			}
			got, err := d.GetFormation(ctx, f.GetId())
			if err != nil {
				t.Fatal(err)
			}
			if got.GetGeneration() != f.GetGeneration() || got.GetObservedGeneration() != f.GetObservedGeneration() || got.GetRelaunches() != f.GetRelaunches() || len(got.GetConditions()) != len(f.GetConditions()) {
				t.Fatalf("formation %v, want %v", got, f)
			}
			for i, c := range f.GetConditions() {
				if !proto.Equal(c, got.GetConditions()[i]) {
					t.Fatalf("condition %d %v, want %v", i, got.GetConditions()[i], c)
				}
			}
			sameRequestStatus(t, got.GetRequest(), f.GetRequest())
		}},
	}
	for _, tc := range cases {
		for _, full := range []bool{true, false} {
			t.Run(tc.name+"/"+word(full), func(t *testing.T) { tc.check(t, full) })
		}
	}
}

// Fails unless the route limits, route shaping and idle window match
func sameRequestStatus(t *testing.T, got, want *v1.RunRequest) {
	t.Helper()
	if !proto.Equal(got.GetPolicy(), want.GetPolicy()) || !proto.Equal(got.GetRouteProfile(), want.GetRouteProfile()) || got.GetIdleUnloadSeconds() != want.GetIdleUnloadSeconds() {
		t.Fatalf("request %v, want %v", got, want)
	}
}

// Names a case variant for ids and subtests
func word(full bool) string {
	if full {
		return "full"
	}
	return "bare"
}
