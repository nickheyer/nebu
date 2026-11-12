package installs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// An install a slot, a live formation or a live instance names is refused removal until nothing names it
func TestRemoveRefusesNamedInstall(t *testing.T) {
	m := buildManager(t)
	ctx := context.Background()
	bin := filepath.Join(t.TempDir(), "fake-server")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	in, err := m.Adopt(ctx, "fake", bin)
	if err != nil {
		t.Fatal(err)
	}
	id := in.GetId()
	now := timestamppb.Now()
	tests := []struct {
		name    string
		hold    func(t *testing.T)
		release func(t *testing.T)
	}{
		{name: "slot", hold: func(t *testing.T) {
			if err := m.DB.PutSlot(ctx, &v1.Slot{Id: "s1", Name: "main", RuntimeId: "fake", InstallId: id, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
		}, release: func(t *testing.T) {
			if _, err := m.DB.DeleteSlot(ctx, "s1"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "formation seat", hold: func(t *testing.T) {
			if err := m.DB.PutFormation(ctx, &v1.Formation{Id: "f1", Name: "pair", State: v1.FormationState_FORMATION_STATE_READY, Seats: []*v1.Seat{{Role: "decode", InstallId: id}}, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
		}, release: func(t *testing.T) {
			if err := m.DB.PutFormation(ctx, &v1.Formation{Id: "f1", Name: "pair", State: v1.FormationState_FORMATION_STATE_STOPPED, Seats: []*v1.Seat{{Role: "decode", InstallId: id}}, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "running instance", hold: func(t *testing.T) {
			if err := m.DB.PutInstance(ctx, &v1.Instance{Id: "i1", Name: "solo", RuntimeId: "fake", InstallId: id, State: v1.InstanceState_INSTANCE_STATE_READY, CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
		}, release: func(t *testing.T) {
			if err := m.DB.PutInstance(ctx, &v1.Instance{Id: "i1", Name: "solo", RuntimeId: "fake", InstallId: id, State: v1.InstanceState_INSTANCE_STATE_STOPPED, CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.hold(t)
			if _, err := m.Remove(ctx, id); !errors.Is(err, ErrInUse) {
				t.Fatalf("removal while named: %v", err)
			}
			if _, err := m.Get(ctx, id); err != nil {
				t.Fatalf("the refused removal dropped the install: %v", err)
			}
			tt.release(t)
		})
	}
	if _, err := m.Remove(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(ctx, id); !errors.Is(err, ErrUnknownInstall) {
		t.Fatalf("after removal: %v", err)
	}
}
