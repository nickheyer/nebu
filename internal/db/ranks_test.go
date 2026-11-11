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

// Ranks of an instance and of a formation seat come back from the store unchanged, none for one rank
func TestRanksRoundTrip(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if len(d.Drift) != 0 {
		t.Fatalf("drift after migrating: %v", d.Drift)
	}
	ctx := context.Background()
	now := timestamppb.New(time.Now().UTC().Truncate(time.Second))
	two := []*v1.InstanceRank{
		{Rank: 0, Endpoint: "http://127.0.0.1:8000", Pid: 11, Command: []string{"vllm", "serve", "--port", "8000"}, Healthy: true, Port: 9000},
		{Rank: 1, Endpoint: "http://127.0.0.1:8001", Pid: 12, Command: []string{"vllm", "serve", "--port", "8001"}, Port: 9001},
	}
	for _, c := range []struct {
		name  string
		ranks []*v1.InstanceRank
	}{{"one rank", nil}, {"two ranks", two}} {
		t.Run(c.name, func(t *testing.T) {
			in := &v1.Instance{Id: "in-" + c.name, Name: c.name, State: v1.InstanceState_INSTANCE_STATE_READY, CreatedAt: now, Ranks: c.ranks}
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
			if got == nil || len(got.GetRanks()) != len(c.ranks) {
				t.Fatalf("instance %v", got)
			}
			for i, r := range c.ranks {
				if !proto.Equal(r, got.GetRanks()[i]) {
					t.Fatalf("rank %d %v, want %v", i, got.GetRanks()[i], r)
				}
			}
			f := &v1.Formation{Id: "f-" + c.name, Name: "reps", State: v1.FormationState_FORMATION_STATE_READY, CreatedAt: now, UpdatedAt: now, Seats: []*v1.Seat{{NodeId: "n1", Role: "replica", InstanceId: in.GetId(), Ranks: c.ranks}}}
			if err := d.PutFormation(ctx, f); err != nil {
				t.Fatal(err)
			}
			back, err := d.GetFormation(ctx, f.GetId())
			if err != nil {
				t.Fatal(err)
			}
			if len(back.GetSeats()) != 1 || len(back.GetSeats()[0].GetRanks()) != len(c.ranks) {
				t.Fatalf("seat %v", back.GetSeats())
			}
			for i, r := range c.ranks {
				if !proto.Equal(r, back.GetSeats()[0].GetRanks()[i]) {
					t.Fatalf("seat rank %d %v, want %v", i, back.GetSeats()[0].GetRanks()[i], r)
				}
			}
		})
	}
}
