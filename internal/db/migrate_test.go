package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"
)

// The embedded migration files in order
func migrationFilesList(t *testing.T) []migrate.File {
	t.Helper()
	dir, err := migrationDir()
	if err != nil {
		t.Fatal(err)
	}
	files, err := dir.Files()
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// A raw connection with the revision table, count more pending migrations applied
func applyMore(t *testing.T, path string, count int) *sql.DB {
	t.Helper()
	ctx := context.Background()
	sqldb, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	conn, err := sqldb.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, revisionTableDDL); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		return sqldb
	}
	dir, err := migrationDir()
	if err != nil {
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
	if err := ex.ExecuteN(ctx, count); err != nil {
		t.Fatal(err)
	}
	return sqldb
}

// The live schema of a database file without the revision table
func inspect(t *testing.T, path string) (*schema.Realm, migrate.Driver) {
	t.Helper()
	ctx := context.Background()
	sqldb, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	sqldb.SetMaxOpenConns(1)
	drv, err := sqlite.Open(sqldb)
	if err != nil {
		t.Fatal(err)
	}
	realm, err := drv.InspectRealm(ctx, &schema.InspectRealmOption{Exclude: []string{"main." + revisionTable}})
	if err != nil {
		t.Fatal(err)
	}
	return realm, drv
}

// Applied revision versions in order
func revisions(t *testing.T, d *DB) []string {
	t.Helper()
	versions, err := d.strings(context.Background(), `SELECT version FROM "`+revisionTable+`" ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	return versions
}

// Every migration's down file restores the schema its up file started from, one step at a time from head
func TestDownMigrationsReverseEachUp(t *testing.T) {
	ctx := context.Background()
	files := migrationFilesList(t)
	if len(files) < 27 {
		t.Fatalf("%d migration files", len(files))
	}
	want := make([]*schema.Realm, len(files)+1)
	up := filepath.Join(t.TempDir(), "up.db")
	for i := range files {
		applyMore(t, up, min(i, 1)).Close()
		want[i], _ = inspect(t, up)
	}
	down := filepath.Join(t.TempDir(), "down.db")
	d, err := Open(down)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.MigrateDown(ctx, "20991231000000"); err == nil {
		t.Fatal("a version no migration has must be refused")
	}
	for i := len(files) - 1; i >= 0; i-- {
		target := ""
		if i > 0 {
			target = files[i-1].Version()
		}
		t.Run(files[i].Name(), func(t *testing.T) {
			if err := d.MigrateDown(ctx, target); err != nil {
				t.Fatal(err)
			}
			if got := revisions(t, d); len(got) != i || i > 0 && got[i-1] != target {
				t.Fatalf("revisions after down %v", got)
			}
			have, drv := inspect(t, down)
			changes, err := drv.RealmDiff(have, want[i])
			if err != nil {
				t.Fatal(err)
			}
			if len(changes) != 0 {
				t.Fatalf("schema after reverting %s differs from the one before it: %v", files[i].Name(), describe(changes))
			}
		})
	}
	// Down to nothing then up again lands at head with no drift
	again, err := Open(down, func(ctx context.Context, d *DB) error { return d.DropLegacyChatFiles(ctx) })
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if len(again.Drift) != 0 || again.Baselined != "" || again.Upgraded != "" || len(revisions(t, again)) != len(files) {
		t.Fatalf("after migrating up again drift %v baselined %q upgraded %q revisions %d", again.Drift, again.Baselined, again.Upgraded, len(revisions(t, again)))
	}
}

// A database from the runner before atlas keeps its file and rows, gains every migration, and leaves a copy of itself
func TestLegacyDatabaseUpgradesInPlace(t *testing.T) {
	ctx := context.Background()
	files := migrationFilesList(t)
	path := filepath.Join(t.TempDir(), "nebu.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	stmts, err := files[0].Stmts()
	if err != nil {
		t.Fatal(err)
	}
	stmts = append(stmts,
		`CREATE TABLE schema_migrations (version TEXT NOT NULL PRIMARY KEY)`,
		`INSERT INTO schema_migrations (version) VALUES ('0012_routes')`,
		`ALTER TABLE slots DROP COLUMN burst`,
		`INSERT INTO slots (id, name, state, created_at, updated_at) VALUES ('s1', 'main', 'ready', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO routes (name, instance_id, slot_id, state, updated_at) VALUES ('main', 'i1', 's1', 'ready', '2026-01-01T00:00:00Z')`,
		`INSERT INTO settings (key, value) VALUES ('k', 'v')`,
	)
	for _, stmt := range stmts {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	raw.Close()
	d, err := Open(path, func(ctx context.Context, d *DB) error { return d.DropLegacyChatFiles(ctx) })
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !strings.Contains(d.Upgraded, "brought to "+files[0].Version()+" in place with 1 changes") || !strings.Contains(d.Upgraded, "pre-atlas") {
		t.Fatalf("upgrade note %q", d.Upgraded)
	}
	if d.Baselined != "" || len(d.Drift) != 0 {
		t.Fatalf("baselined %q drift %v", d.Baselined, d.Drift)
	}
	if got := revisions(t, d); len(got) != len(files) || got[0] != files[0].Version() || got[len(got)-1] != files[len(files)-1].Version() {
		t.Fatalf("revisions %v", got)
	}
	var kind int
	if err := d.sql.QueryRow(`SELECT type FROM "`+revisionTable+`" WHERE version = ?`, files[0].Version()).Scan(&kind); err != nil || kind != int(migrate.RevisionTypeBaseline) {
		t.Fatalf("first revision type %d %v", kind, err)
	}
	if n, err := d.strings(ctx, `SELECT name FROM sqlite_master WHERE name = 'schema_migrations'`); err != nil || len(n) != 0 {
		t.Fatalf("legacy table still there %v %v", n, err)
	}
	routes, err := d.ListRoutes(ctx)
	if err != nil || len(routes) != 1 || routes[0].GetName() != "main" || routes[0].GetOwnerKind() != "slot" || routes[0].GetOwnerId() != "s1" {
		t.Fatalf("routes after the legacy upgrade %v %v", routes, err)
	}
	slots, err := d.ListSlots(ctx)
	if err != nil || len(slots) != 1 || slots[0].GetName() != "main" {
		t.Fatalf("slots after the legacy upgrade %v %v", slots, err)
	}
	if settings, err := d.stringMap(ctx, `SELECT key, value FROM settings`); err != nil || settings["k"] != "v" {
		t.Fatalf("settings after the legacy upgrade %v %v", settings, err)
	}
	copies, err := filepath.Glob(path + ".pre-atlas.*")
	if err != nil || len(copies) != 1 {
		t.Fatalf("backup copies %v %v", copies, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the database was moved: %v", err)
	}
	copyDB, err := sql.Open("sqlite", "file:"+copies[0])
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var legacy string
	if err := copyDB.QueryRow(`SELECT version FROM schema_migrations`).Scan(&legacy); err != nil || legacy != "0012_routes" {
		t.Fatalf("the copy keeps the legacy state: %q %v", legacy, err)
	}
}

// Upgrading carries each route's slot or instance into its owner, drops rows nothing held, and the down puts the columns back
func TestRouteUpgradeCarriesOwner(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nebu.db")
	old := openBefore(t, path, "20261103060020_route_resources")
	for _, stmt := range []string{
		`INSERT INTO routes (name, instance_id, slot_id, endpoint, api, state, model, requests, updated_at, served) VALUES ('main', 'i1', 's1', 'http://127.0.0.1:9000', 'openai', 'ready', 'm', 3, '2026-10-01T00:00:00Z', 'main')`,
		`INSERT INTO routes (name, instance_id, state, updated_at) VALUES ('solo', 'i2', 'ready', '2026-10-01T00:00:00Z')`,
		`INSERT INTO routes (name, state, updated_at) VALUES ('orphan', 'ready', '2026-10-01T00:00:00Z')`,
	} {
		if _, err := old.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	old.Close()
	d, err := Open(path, func(ctx context.Context, d *DB) error { return d.DropLegacyChatFiles(ctx) })
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	routes, err := d.ListRoutes(ctx)
	if err != nil || len(routes) != 2 {
		t.Fatalf("routes %v %v", routes, err)
	}
	for _, tc := range []struct{ name, kind, id string }{{"main", "slot", "s1"}, {"solo", "instance", "i2"}} {
		var found bool
		for _, r := range routes {
			if r.GetName() == tc.name {
				found = true
				if r.GetOwnerKind() != tc.kind || r.GetOwnerId() != tc.id || r.GetMeta().GetCreatedAt() == nil {
					t.Fatalf("route %s upgraded to %v", tc.name, r)
				}
			}
		}
		if !found {
			t.Fatalf("route %s lost", tc.name)
		}
	}
	if err := d.MigrateDown(ctx, "20261102120000"); err != nil {
		t.Fatal(err)
	}
	owners, err := d.stringMap(ctx, `SELECT name, instance_id || '/' || slot_id FROM routes`)
	if err != nil || len(owners) != 2 || owners["main"] != "/s1" || owners["solo"] != "i2/" {
		t.Fatalf("routes after the down %v %v", owners, err)
	}
}
