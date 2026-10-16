package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/sqlite"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Opens a database with every migration but the last applied
func openBeforeLast(t *testing.T, path string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	sqldb, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := migrationDir()
	if err != nil {
		t.Fatal(err)
	}
	files, err := dir.Files()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sqldb.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, revisionTableDDL); err != nil {
		t.Fatal(err)
	}
	drv, err := sqlite.Open(conn)
	if err != nil {
		t.Fatal(err)
	}
	ex, err := migrate.NewExecutor(drv, dir, revisionStore{conn}, migrate.WithOperatorVersion(operator))
	if err != nil {
		t.Fatal(err)
	}
	if err := ex.ExecuteN(ctx, len(files)-1); err != nil {
		t.Fatal(err)
	}
	return sqldb
}

// The old shaping columns fold into the profile JSON, allow all only when it was on, bare rows staying bare
func TestProfileMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nebu.db")
	old := openBeforeLast(t, path)
	for _, stmt := range []string{
		`INSERT INTO slots (id, name, state, created_at, updated_at, system_messages, allow_all) VALUES ('s1', 'shaped', 'ready', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'merge', 1)`,
		`INSERT INTO slots (id, name, state, created_at, updated_at) VALUES ('s2', 'plain', 'empty', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO slot_aliases (slot_id, position, name, system_messages, allow_all) VALUES ('s1', 0, 'alias', 'user', 0)`,
		`INSERT INTO routes (name, state, updated_at, system_messages, allow_all) VALUES ('r1', 'ready', '2026-01-01T00:00:00Z', '', 1)`,
		`INSERT INTO routes (name, state, updated_at) VALUES ('r2', 'ready', '2026-01-01T00:00:00Z')`,
	} {
		if _, err := old.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	old.Close()
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if len(d.Drift) != 0 {
		t.Fatalf("drift after migrating: %v", d.Drift)
	}
	slots, err := d.ListSlots(context.Background())
	if err != nil || len(slots) != 2 {
		t.Fatalf("slots %v: %v", slots, err)
	}
	byName := map[string]*v1.Slot{}
	for _, s := range slots {
		byName[s.GetName()] = s
	}
	if want := (&v1.Profile{SystemMessages: v1.SystemMessages_SYSTEM_MESSAGES_MERGE, AllowAll: proto.Bool(true)}); !proto.Equal(byName["shaped"].GetProfile(), want) {
		t.Fatalf("shaped slot profile %v", byName["shaped"].GetProfile())
	}
	if byName["plain"].GetProfile() != nil {
		t.Fatalf("plain slot gained a profile %v", byName["plain"].GetProfile())
	}
	aliases := byName["shaped"].GetAliases()
	if len(aliases) != 1 || !proto.Equal(aliases[0].GetProfile(), &v1.Profile{SystemMessages: v1.SystemMessages_SYSTEM_MESSAGES_USER}) {
		t.Fatalf("alias profile %v", aliases)
	}
	routes, err := d.ListRoutes(context.Background())
	if err != nil || len(routes) != 2 {
		t.Fatalf("routes %v: %v", routes, err)
	}
	if !proto.Equal(routes[0].GetProfile(), &v1.Profile{AllowAll: proto.Bool(true)}) || routes[1].GetProfile() != nil {
		t.Fatalf("route profiles %v / %v", routes[0].GetProfile(), routes[1].GetProfile())
	}
}

// A profile round trips whole, tool servers and shaping included
func TestProfileRoundTrip(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	profile := &v1.Profile{SystemMessages: v1.SystemMessages_SYSTEM_MESSAGES_KEEP, AllowAll: proto.Bool(false), ToolServers: []string{"web", "node/fetch"}, MutatingTools: true, SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_REPLACE, SystemText: "You are terse.", StripExtras: proto.Bool(true), Overrides: `{"temperature":0.6}`}
	s := &v1.Slot{Id: "s1", Name: "main", State: v1.SlotState_SLOT_STATE_EMPTY, CreatedAt: timestamppb.New(time.Unix(1, 0)), UpdatedAt: timestamppb.New(time.Unix(2, 0)), Profile: profile, Aliases: []*v1.SlotAlias{{Name: "alt", Profile: &v1.Profile{SystemPrompt: v1.SystemPrompt_SYSTEM_PROMPT_DROP, StripExtras: proto.Bool(false)}}}}
	if err := d.PutSlot(ctx, s); err != nil {
		t.Fatal(err)
	}
	slots, err := d.ListSlots(ctx)
	if err != nil || len(slots) != 1 || !proto.Equal(slots[0], s) {
		t.Fatalf("slot round trip\n got %v\nwant %v\nerr %v", slots, s, err)
	}
	r := &v1.Route{Name: "main", State: v1.RouteState_ROUTE_STATE_READY, UpdatedAt: timestamppb.New(time.Unix(3, 0)), Profile: profile}
	if err := d.PutRoute(ctx, r); err != nil {
		t.Fatal(err)
	}
	routes, err := d.ListRoutes(ctx)
	if err != nil || len(routes) != 1 || !proto.Equal(routes[0], r) {
		t.Fatalf("route round trip %v: %v", routes, err)
	}
}
