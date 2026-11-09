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

// Install, objective and scheduler config statuses are stored with their conditions and read back, none staying none
func TestResourceStatusPersists(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	at := timestamppb.New(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	conds := []*v1.Condition{{Type: "Accepted", Status: v1.ConditionStatus_CONDITION_STATUS_TRUE, Reason: "Accepted", Message: "m", LastTransitionTime: at, ObservedGeneration: 2}, {Type: "Ready", Status: v1.ConditionStatus_CONDITION_STATUS_FALSE, Reason: "BinaryMissing", LastTransitionTime: at, ObservedGeneration: 2}}
	meta := &v1.ObjectMeta{Generation: 2, CreatedAt: at, UpdatedAt: at}
	t.Run("install", func(t *testing.T) {
		tests := []struct {
			name string
			in   *v1.Install
		}{
			{name: "with status", in: &v1.Install{Id: "i1", RuntimeId: "rt", Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Path: "/a", Dir: "/", CreatedAt: at, Generation: 2, Status: &v1.InstallStatus{Conditions: conds, ObservedGeneration: 2}}},
			{name: "without status", in: &v1.Install{Id: "i2", RuntimeId: "rt", Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Path: "/b", Dir: "/", CreatedAt: at}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if err := d.PutInstall(ctx, tt.in); err != nil {
					t.Fatal(err)
				}
				got, err := d.GetInstall(ctx, tt.in.GetId())
				if err != nil {
					t.Fatal(err)
				}
				if got.GetGeneration() != tt.in.GetGeneration() || !proto.Equal(got.GetStatus(), tt.in.GetStatus()) || (got.GetStatus() == nil) != (tt.in.GetStatus() == nil) {
					t.Fatalf("got generation %d status %v, want %d %v", got.GetGeneration(), got.GetStatus(), tt.in.GetGeneration(), tt.in.GetStatus())
				}
			})
		}
	})
	t.Run("objective", func(t *testing.T) {
		o := &v1.Objective{Name: "fast", Priority: 3, Meta: meta, Status: &v1.ObjectiveStatus{Conditions: conds[:1], ObservedGeneration: 2}}
		if err := d.PutObjective(ctx, o); err != nil {
			t.Fatal(err)
		}
		bare := &v1.Objective{Name: "bare", Meta: meta}
		if err := d.PutObjective(ctx, bare); err != nil {
			t.Fatal(err)
		}
		list, err := d.ListObjectives(ctx)
		if err != nil || len(list) != 2 {
			t.Fatalf("objectives %v %v", list, err)
		}
		if list[0].GetStatus() != nil {
			t.Fatalf("an objective stored without a status reads one back: %v", list[0].GetStatus())
		}
		if !proto.Equal(list[1].GetStatus(), o.GetStatus()) {
			t.Fatalf("objective status %v, want %v", list[1].GetStatus(), o.GetStatus())
		}
		o.Status = &v1.ObjectiveStatus{Conditions: conds, ObservedGeneration: 3}
		if err := d.PutObjective(ctx, o); err != nil {
			t.Fatal(err)
		}
		if list, _ := d.ListObjectives(ctx); !proto.Equal(list[1].GetStatus(), o.GetStatus()) {
			t.Fatalf("replaced objective status %v, want %v", list[1].GetStatus(), o.GetStatus())
		}
	})
	t.Run("scheduler config", func(t *testing.T) {
		c := &v1.SchedulerConfig{Name: "mine", Spec: &v1.SchedulerConfigSpec{Yaml: "plugins: []\n"}, Meta: meta, Status: &v1.SchedulerConfigStatus{Conditions: conds[:1], ObservedGeneration: 2}}
		if err := d.PutSchedulerConfig(ctx, c); err != nil {
			t.Fatal(err)
		}
		list, err := d.ListSchedulerConfigs(ctx)
		if err != nil || len(list) != 1 {
			t.Fatalf("configs %v %v", list, err)
		}
		if list[0].GetSpec().GetYaml() != "plugins: []\n" || !proto.Equal(list[0].GetStatus(), c.GetStatus()) {
			t.Fatalf("config read back %v", list[0])
		}
	})
}
