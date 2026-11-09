// Package db is the daemon's store, pure Go SQLite.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql migrations/atlas.sum migrations/down/*.sql
var migrationFiles embed.FS

// Target schema for Atlas migrations and drift checks.
//
//go:embed schema.sql
var desiredSchema string

const (
	operator = "nebu"
	// Connections reads share beside the one writer, each on its own WAL snapshot
	readConns = 4
	// The table the migration runner before atlas recorded its versions in
	legacyTable = "schema_migrations"
	// Where the reverse of each migration lives, by the migration's file name
	downDir = "migrations/down"
)

// Returned when a record is not in the store
var ErrNotFound = errors.New("not found")

// Reports whether err means a missing record
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// Open store
type DB struct {
	// The writer, one connection so writes queue instead of failing busy
	sql *sql.DB
	// The readers, a pool that never waits behind the writer
	reads *sql.DB
	path  string
	// How a database from the migration runner before atlas was brought onto the chain in place, or empty
	Upgraded string
	// Differences between the live schema and schema.sql.
	Drift []string
	// Recovery details for revisions missing from the migration directory, or empty.
	Baselined string
}

// Work a release does on the data once its migrations have run, before the schema is checked
type Upgrade func(ctx context.Context, d *DB) error

// Opens the database, applies embedded migrations, and runs the upgrades over the migrated rows
func Open(path string, upgrades ...Upgrade) (*DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqldb.SetMaxOpenConns(1)
	reads, err := sql.Open("sqlite", dsn)
	if err != nil {
		sqldb.Close()
		return nil, err
	}
	reads.SetMaxOpenConns(readConns)
	reads.SetMaxIdleConns(readConns)
	d := &DB{sql: sqldb, reads: reads, path: path}
	if err := d.migrate(context.Background(), upgrades); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// Closes the readers and the writer
func (d *DB) Close() error { return errors.Join(d.reads.Close(), d.sql.Close()) }

// Applies pending migrations, runs the store's own upgrades then the caller's, and notes the drift left
func (d *DB) migrate(ctx context.Context, upgrades []Upgrade) error {
	dir, err := migrationDir()
	if err != nil {
		return err
	}
	if err := migrate.Validate(dir); err != nil {
		return fmt.Errorf("migration directory: %w", err)
	}
	if err := d.apply(ctx, dir); err != nil {
		return err
	}
	// The writer is free again here, so upgrades may write through the store's own methods
	for _, up := range append([]Upgrade{hashLegacyTokens, adminLegacyUsers}, upgrades...) {
		if err := up(ctx, d); err != nil {
			return fmt.Errorf("upgrade: %w", err)
		}
	}
	return d.withDriver(ctx, func(drv migrate.Driver, conn *sql.Conn) error {
		changes, err := schemaChanges(ctx, drv)
		if err != nil {
			return err
		}
		d.Drift = describe(changes)
		return nil
	})
}

// Applies pending migrations with atlas and records them in its revision table
func (d *DB) apply(ctx context.Context, dir migrate.Dir) error {
	return d.withDriver(ctx, func(drv migrate.Driver, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, revisionTableDDL); err != nil {
			return fmt.Errorf("create revision table: %w", err)
		}
		revs := revisionStore{conn}
		var err error
		if d.Upgraded, err = adoptLegacy(ctx, drv, conn, d.path, dir, revs); err != nil {
			return err
		}
		if d.Baselined, err = baseline(ctx, drv, conn, d.path, dir, revs); err != nil {
			return err
		}
		ex, err := migrate.NewExecutor(drv, dir, revs, migrate.WithOperatorVersion(operator))
		if err != nil {
			return err
		}
		if err := ex.ExecuteN(ctx, 0); err != nil && !errors.Is(err, migrate.ErrNoPendingFiles) {
			return fmt.Errorf("migrate: %w", err)
		}
		return nil
	})
}

// Reverts every applied migration newer than version, newest first, each through its down file, all when version is empty
func (d *DB) MigrateDown(ctx context.Context, version string) error {
	dir, err := migrationDir()
	if err != nil {
		return err
	}
	files, err := dir.Files()
	if err != nil {
		return err
	}
	if version != "" && migrate.FilesLastIndex(files, func(f migrate.File) bool { return f.Version() == version }) < 0 {
		return fmt.Errorf("migrate down: no migration has version %s", version)
	}
	return d.withDriver(ctx, func(drv migrate.Driver, conn *sql.Conn) error {
		revs := revisionStore{conn}
		applied, err := revs.ReadRevisions(ctx)
		if err != nil {
			return err
		}
		for i := len(applied) - 1; i >= 0 && applied[i].Version > version; i-- {
			stmts, err := downStatements(files, applied[i].Version)
			if err != nil {
				return err
			}
			if err := execStatements(ctx, conn, stmts); err != nil {
				return fmt.Errorf("migrate down %s: %w", applied[i].Version, err)
			}
			if err := revs.DeleteRevision(ctx, applied[i].Version); err != nil {
				return err
			}
		}
		return nil
	})
}

// The statements reversing one migration, read from its down file
func downStatements(files []migrate.File, version string) ([]string, error) {
	i := migrate.FilesLastIndex(files, func(f migrate.File) bool { return f.Version() == version })
	if i < 0 {
		return nil, fmt.Errorf("migrate down: revision %s is not in the migration directory", version)
	}
	data, err := fs.ReadFile(migrationFiles, downDir+"/"+files[i].Name())
	if err != nil {
		return nil, fmt.Errorf("migrate down: %s has no down file: %w", files[i].Name(), err)
	}
	return migrate.NewLocalFile(files[i].Name(), data).Stmts()
}

// Runs fn with an atlas driver over the writer connection, held for fn alone
func (d *DB) withDriver(ctx context.Context, fn func(drv migrate.Driver, conn *sql.Conn) error) error {
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	drv, err := sqlite.Open(conn)
	if err != nil {
		return err
	}
	return fn(drv, conn)
}

// Brings a database from the runner before atlas onto the chain in place, backed up first, then run through every migration
func adoptLegacy(ctx context.Context, drv migrate.Driver, conn *sql.Conn, path string, dir migrate.Dir, store revisionStore) (string, error) {
	var legacy int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, legacyTable).Scan(&legacy); err != nil {
		return "", err
	}
	if legacy == 0 {
		return "", nil
	}
	all, err := dir.Files()
	if err != nil {
		return "", err
	}
	files := migrate.SkipCheckpointFiles(all)
	if len(files) == 0 {
		return "", errors.New("legacy upgrade: the migration directory is empty")
	}
	backup, err := copyAside(ctx, conn, path, "pre-atlas")
	if err != nil {
		return "", err
	}
	if _, err := conn.ExecContext(ctx, `DROP TABLE "`+legacyTable+`"`); err != nil {
		return "", err
	}
	first := files[0]
	stmts, err := first.Stmts()
	if err != nil {
		return "", err
	}
	changes, err := changesTo(ctx, drv, stmts)
	if err != nil {
		return "", err
	}
	if len(changes) > 0 {
		if err := applyChanges(ctx, drv, conn, changes); err != nil {
			return "", fmt.Errorf("legacy upgrade to %s: %w", first.Version(), err)
		}
	}
	sums, err := dir.Checksum()
	if err != nil {
		return "", err
	}
	sum, err := sums.SumByName(first.Name())
	if err != nil {
		return "", err
	}
	rev := &migrate.Revision{Version: first.Version(), Description: first.Desc(), Type: migrate.RevisionTypeBaseline, ExecutedAt: time.Now(), Hash: sum, OperatorVersion: operator}
	if err := store.WriteRevision(ctx, rev); err != nil {
		return "", err
	}
	return fmt.Sprintf("the %s table named the runner before atlas, so the schema was brought to %s in place with %d changes and every later migration runs, the copy before at %s", legacyTable, first.Version(), len(changes), backup), nil
}

// Reconciles databases whose recorded revisions are absent from the directory.
// The init migration may change before release, so preserve existing rows
// instead of replaying CREATE TABLE statements.
func baseline(ctx context.Context, drv migrate.Driver, conn *sql.Conn, path string, dir migrate.Dir, store revisionStore) (string, error) {
	all, err := dir.Files()
	if err != nil {
		return "", err
	}
	files := migrate.SkipCheckpointFiles(all)
	if len(files) == 0 {
		return "", nil
	}
	sums, err := dir.Checksum()
	if err != nil {
		return "", err
	}
	revs, err := store.ReadRevisions(ctx)
	if err != nil {
		return "", err
	}
	why := ""
	if len(revs) == 0 {
		var dirty *migrate.NotCleanError
		if err := drv.CheckClean(ctx, store.Ident()); errors.As(err, &dirty) {
			why = "the database has a schema but no revisions"
		} else if err != nil {
			return "", err
		}
	}
	for _, r := range revs {
		i := migrate.FilesLastIndex(files, func(f migrate.File) bool { return f.Version() == r.Version })
		if i == -1 {
			why = "revision " + r.Version + " is not in the migration directory"
			break
		}
		if sum, err := sums.SumByName(files[i].Name()); err != nil || sum != r.Hash {
			why = "migration " + r.Version + " was rewritten since it was applied"
			break
		}
	}
	if why == "" {
		return "", nil
	}
	changes, err := schemaChanges(ctx, drv)
	if err != nil {
		return "", err
	}
	if len(changes) > 0 {
		if _, err := copyAside(ctx, conn, path, "pre-baseline"); err != nil {
			return "", err
		}
		if err := applyChanges(ctx, drv, conn, changes); err != nil {
			return "", fmt.Errorf("baseline: %w", err)
		}
	}
	for _, r := range revs {
		if err := store.DeleteRevision(ctx, r.Version); err != nil {
			return "", err
		}
	}
	head := files[len(files)-1]
	sum, err := sums.SumByName(head.Name())
	if err != nil {
		return "", err
	}
	rev := &migrate.Revision{Version: head.Version(), Description: head.Desc(), Type: migrate.RevisionTypeBaseline, ExecutedAt: time.Now(), Hash: sum, OperatorVersion: operator}
	if err := store.WriteRevision(ctx, rev); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s, so the schema was brought to %s in place with %d changes", why, head.Version(), len(changes)), nil
}

// Plans schema changes with atlas and runs them as one transaction between their pragmas
func applyChanges(ctx context.Context, drv migrate.Driver, conn *sql.Conn, changes []schema.Change) error {
	plan, err := drv.PlanChanges(ctx, "baseline", changes)
	if err != nil {
		return err
	}
	stmts := make([]string, 0, len(plan.Changes))
	for _, c := range plan.Changes {
		stmts = append(stmts, c.Cmd)
	}
	return execStatements(ctx, conn, stmts)
}

// Runs the statements as one transaction, pragmas before and after it kept outside so they take effect
func execStatements(ctx context.Context, conn *sql.Conn, stmts []string) error {
	var before, body, after []string
	for _, stmt := range stmts {
		switch {
		case !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(stmt)), "PRAGMA"):
			body = append(body, stmt)
		case len(body) == 0:
			before = append(before, stmt)
		default:
			after = append(after, stmt)
		}
	}
	run := func(stmts []string) error {
		for _, stmt := range stmts {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	}
	if err := run(before); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return err
	}
	if err := run(body); err != nil {
		conn.ExecContext(ctx, "ROLLBACK")
		return errors.Join(err, run(after))
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	return run(after)
}

// Writes a dated backup of the database through VACUUM INTO, a consistent copy made page by page
func copyAside(ctx context.Context, conn *sql.Conn, path, why string) (string, error) {
	if path == "" || path == ":memory:" {
		return "", nil
	}
	aside := fmt.Sprintf("%s.%s.%s", path, why, time.Now().UTC().Format("20060102T150405"))
	if err := os.Remove(aside); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, aside); err != nil {
		return "", fmt.Errorf("backup to %s: %w", aside, err)
	}
	return aside, os.Chmod(aside, 0o600)
}

// Copies the embedded migration files into an atlas memory dir, the down directory left out of its checksum
func migrationDir() (*migrate.MemDir, error) {
	dir := &migrate.MemDir{}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := fs.ReadFile(migrationFiles, "migrations/"+entry.Name())
		if err != nil {
			return nil, err
		}
		if err := dir.WriteFile(entry.Name(), data); err != nil {
			return nil, err
		}
	}
	return dir, nil
}

// Schema changes needed to match schema.sql.
func schemaChanges(ctx context.Context, live migrate.Driver) ([]schema.Change, error) {
	stmts, err := migrate.NewLocalFile("schema.sql", []byte(desiredSchema)).Stmts()
	if err != nil {
		return nil, fmt.Errorf("parse schema.sql: %w", err)
	}
	return changesTo(ctx, live, stmts)
}

// Schema changes bringing the live database to what the statements build in a fresh one
func changesTo(ctx context.Context, live migrate.Driver, stmts []string) ([]schema.Change, error) {
	mem, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer mem.Close()
	mem.SetMaxOpenConns(1)
	for _, stmt := range stmts {
		if _, err := mem.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("replay %q: %w", stmt, err)
		}
	}
	wantDrv, err := sqlite.Open(mem)
	if err != nil {
		return nil, err
	}
	want, err := wantDrv.InspectRealm(ctx, nil)
	if err != nil {
		return nil, err
	}
	have, err := live.InspectRealm(ctx, &schema.InspectRealmOption{Exclude: []string{"main." + revisionTable}})
	if err != nil {
		return nil, err
	}
	return live.RealmDiff(have, want)
}

// Formats one schema change per line.
func describe(changes []schema.Change) []string {
	var out []string
	for _, c := range changes {
		switch c := c.(type) {
		case *schema.AddTable:
			out = append(out, "table "+c.T.Name+" missing")
		case *schema.DropTable:
			out = append(out, "table "+c.T.Name+" unexpected")
		case *schema.ModifyTable:
			for _, sub := range c.Changes {
				out = append(out, "table "+c.T.Name+" "+describeChange(sub))
			}
		default:
			out = append(out, fmt.Sprintf("%T", c))
		}
	}
	return out
}

func describeChange(c schema.Change) string {
	switch c := c.(type) {
	case *schema.AddColumn:
		return "column " + c.C.Name + " missing"
	case *schema.DropColumn:
		return "column " + c.C.Name + " unexpected"
	case *schema.ModifyColumn:
		return "column " + c.From.Name + " differs"
	case *schema.AddIndex:
		return "index " + c.I.Name + " missing"
	case *schema.DropIndex:
		return "index " + c.I.Name + " unexpected"
	case *schema.ModifyIndex:
		return "index " + c.From.Name + " differs"
	case *schema.AddForeignKey:
		return "foreign key " + c.F.Symbol + " missing"
	case *schema.DropForeignKey:
		return "foreign key " + c.F.Symbol + " unexpected"
	case *schema.ModifyForeignKey:
		return "foreign key " + c.From.Symbol + " differs"
	case *schema.AddPrimaryKey, *schema.DropPrimaryKey, *schema.ModifyPrimaryKey:
		return "primary key differs"
	}
	return fmt.Sprintf("%T", c)
}
