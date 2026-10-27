package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/sqlite"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Opens a database with every migration before the named one applied
func openBefore(t *testing.T, path string, before string) *sql.DB {
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
	n := -1
	for i, f := range files {
		if strings.HasPrefix(f.Name(), before) {
			n = i
		}
	}
	if n < 0 {
		t.Fatalf("no migration named %s", before)
	}
	if err := ex.ExecuteN(ctx, n); err != nil {
		t.Fatal(err)
	}
	return sqldb
}

// The old shaping columns fold into the profile JSON, bare rows staying bare
func TestProfileMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nebu.db")
	old := openBefore(t, path, "20261016033855_profile_json")
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
	// The daemon's chat image upgrade drops the table the newest migration leaves for it
	d, err := Open(path, func(ctx context.Context, d *DB) error { return d.DropLegacyChatFiles(ctx) })
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
	if want := (&v1.Profile{SystemMessages: v1.SystemMessages_SYSTEM_MESSAGES_MERGE}); !proto.Equal(byName["shaped"].GetProfile(), want) {
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
	if !proto.Equal(routes[0].GetProfile(), &v1.Profile{}) || routes[1].GetProfile() != nil {
		t.Fatalf("route profiles %v / %v", routes[0].GetProfile(), routes[1].GetProfile())
	}
}

// A profile round trips whole, tool servers and system message mode included
func TestProfileRoundTrip(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	profile := &v1.Profile{SystemMessages: v1.SystemMessages_SYSTEM_MESSAGES_KEEP, ToolServers: []string{"web", "node/fetch"}, MutatingTools: true}
	s := &v1.Slot{Id: "s1", Name: "main", State: v1.SlotState_SLOT_STATE_EMPTY, CreatedAt: timestamppb.New(time.Unix(1, 0)), UpdatedAt: timestamppb.New(time.Unix(2, 0)), Profile: profile, Aliases: []*v1.SlotAlias{{Name: "alt", Profile: &v1.Profile{SystemMessages: v1.SystemMessages_SYSTEM_MESSAGES_USER}}}}
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

// Image bytes from before the content store move stay readable until an upgrade has placed them
func TestChatFilesMigrationKeepsBytesForTheUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nebu.db")
	old := openBefore(t, path, "20261022000000_bot_activity_chat_blobs")
	if _, err := old.Exec(`INSERT INTO chat_files (id, provider, subject, media_type, name, width, height, data, created_at) VALUES ('img1', 'local', 'u1', 'image/png', 'a.png', 2, 3, X'0102030405', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	ctx := context.Background()
	moved := 0
	d, err := Open(path, func(ctx context.Context, d *DB) error {
		files, err := d.LegacyChatFiles(ctx)
		if err != nil {
			return err
		}
		for _, f := range files {
			if f.ID != "img1" || string(f.Data) != "\x01\x02\x03\x04\x05" {
				t.Fatalf("legacy row %+v", f)
			}
			moved++
			if err := d.SetChatFileDigest(ctx, f.ID, "sha256:abc"); err != nil {
				return err
			}
		}
		return d.DropLegacyChatFiles(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if moved != 1 || len(d.Drift) != 0 {
		t.Fatalf("moved %d drift %v", moved, d.Drift)
	}
	owner := ChatOwner{Provider: "local", Subject: "u1"}
	f, digest, err := d.GetChatFile(ctx, owner, "img1")
	if err != nil || digest != "sha256:abc" || f.GetSizeBytes() != 5 || f.GetWidth() != 2 || f.GetName() != "a.png" {
		t.Fatalf("migrated file %v %q %v", f, digest, err)
	}
	if files, err := d.LegacyChatFiles(ctx); err != nil || files != nil {
		t.Fatalf("legacy table should be gone: %v %v", files, err)
	}
	if digests, err := d.ChatFileDigests(ctx); err != nil || len(digests) != 1 || digests[0] != "sha256:abc" {
		t.Fatalf("digests %v %v", digests, err)
	}
}

func TestChatFileRowsShareAndOrphanDigests(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	ann, bob := ChatOwner{Provider: "local", Subject: "ann"}, ChatOwner{Provider: "local", Subject: "bob"}
	for _, f := range []struct {
		owner  ChatOwner
		id     string
		digest string
	}{{ann, "a1", "sha256:one"}, {ann, "a2", "sha256:one"}, {ann, "a3", "sha256:two"}, {bob, "b1", "sha256:two"}} {
		ok, err := d.PutChatFile(ctx, f.owner, &v1.ChatFile{Id: f.id, MediaType: "image/png", SizeBytes: 3}, f.digest, "2026-01-01T00:00:00Z")
		if err != nil || !ok {
			t.Fatalf("put %s: %v %v", f.id, ok, err)
		}
	}
	if ok, err := d.PutChatFile(ctx, bob, &v1.ChatFile{Id: "a1", MediaType: "image/png"}, "sha256:x", "2026-01-01T00:00:00Z"); err != nil || ok {
		t.Fatalf("another account's id should be refused: %v %v", ok, err)
	}
	orphaned, err := d.DeleteChatFiles(ctx, ann, []string{"a1", "a3", "b1", "missing"})
	if err != nil || len(orphaned) != 0 {
		t.Fatalf("digests still in use elsewhere: %v %v", orphaned, err)
	}
	if _, _, err := d.GetChatFile(ctx, bob, "b1"); err != nil {
		t.Fatalf("bob's file should survive ann's delete: %v", err)
	}
	orphaned, err = d.DeleteChatFiles(ctx, ann, []string{"a2"})
	if err != nil || len(orphaned) != 1 || orphaned[0] != "sha256:one" {
		t.Fatalf("last holder frees the digest: %v %v", orphaned, err)
	}
	if digests, _ := d.ChatFileDigests(ctx); len(digests) != 1 || digests[0] != "sha256:two" {
		t.Fatalf("digests %v", digests)
	}
}

func TestBotActivityRowsKeepTheNewest(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	if err := d.PutBot(ctx, &v1.Bot{Id: "b1", Name: "nova", CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now()}, "tok"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		a := &v1.BotActivity{BotId: "b1", At: timestamppb.New(time.Unix(int64(i), 0)), Level: "info", Kind: "reply", Message: string(rune('a' + i)), ChannelId: "c1", Trace: "t"}
		if err := d.PutBotActivity(ctx, a, 3); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.ListBotActivity(ctx, "b1", 0)
	if err != nil || len(rows) != 3 || rows[0].GetMessage() != "e" || rows[2].GetMessage() != "c" || rows[0].GetChannelId() != "c1" || rows[0].GetAt().AsTime().Unix() != 4 {
		t.Fatalf("rows %v %v", rows, err)
	}
	if rows, _ := d.ListBotActivity(ctx, "b1", 2); len(rows) != 2 || rows[1].GetMessage() != "d" {
		t.Fatalf("limited rows %v", rows)
	}
	if _, err := d.DeleteBot(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if rows, _ := d.ListBotActivity(ctx, "b1", 0); len(rows) != 0 {
		t.Fatalf("activity should go with the bot: %v", rows)
	}
}
