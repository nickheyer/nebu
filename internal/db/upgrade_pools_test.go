package db_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/events"
)

// A slot names the route it held before the rewrite again once the upgraded row carries it as the owner
func TestUpgradedRouteNamedAgainByItsSlot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nebu.db")
	old := db.OpenBefore(t, path, "20261103060020_route_resources")
	if _, err := old.Exec(`INSERT INTO routes (name, instance_id, slot_id, endpoint, api, state, model, requests, updated_at, served) VALUES ('main', 'i1', 's1', 'http://127.0.0.1:9000', 'openai', 'ready', 'm', 3, '2026-10-01T00:00:00Z', 'main')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	d, err := db.Open(path, func(ctx context.Context, d *db.DB) error { return d.DropLegacyChatFiles(ctx) })
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	m, err := pools.New(ctx, pools.Options{DB: d, Events: events.New(), NodeID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	slot := pools.OwnerRef{Kind: pools.OwnerSlot, ID: "s1"}
	member := pools.Membership{Model: pools.ModelKey{SourceID: "hf", Repo: "org/model", Group: "q4"}, Runtime: "vllm", Protocol: datalayer.AppProtocolHTTP, Concurrency: 4}
	if err := m.Publish(ctx, slot, member, nil); err != nil {
		t.Fatal(err)
	}
	route, err := m.NameRoute(ctx, slot, "main", pools.RouteSpec{})
	if err != nil {
		t.Fatalf("NameRoute after upgrade: %v", err)
	}
	if route.GetOwnerKind() != pools.OwnerSlot || route.GetOwnerId() != "s1" || len(route.GetSpec().GetRules()) != 1 {
		t.Fatalf("route after naming %v", route)
	}
	other := pools.OwnerRef{Kind: pools.OwnerSlot, ID: "s2"}
	if err := m.Publish(ctx, other, member, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NameRoute(ctx, other, "main", pools.RouteSpec{}); err == nil {
		t.Fatal("another slot must not take the upgraded route")
	}
}
