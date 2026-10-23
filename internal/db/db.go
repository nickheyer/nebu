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

//go:embed migrations/*.sql migrations/atlas.sum
var migrationFiles embed.FS

// Target schema for Atlas migrations and drift checks.
//
//go:embed schema.sql
var desiredSchema string

const (
	operator = "nebu"
	// Connections reads share beside the one writer, each on its own WAL snapshot
	readConns = 4
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
	// Backup path for a pre-Atlas database, or empty.
	SetAside string
	// Differences between the live schema and schema.sql.
	Drift []string
	// Recovery details for revisions missing from the migration directory, or empty.
	Baselined string
}

// Work a release does on the data once its migrations have run, before the schema is checked
type Upgrade func(ctx context.Context, d *DB) error

// Opens the database, applies embedded migrations, and runs the upgrades over the migrated rows
func Open(path string, upgrades ...Upgrade) (*DB, error) {
	aside, err := setAsideLegacy(path)
	if err != nil {
		return nil, err
	}
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
	d := &DB{sql: sqldb, reads: reads, path: path, SetAside: aside}
	if err := d.migrate(context.Background(), upgrades); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// Closes the readers and the writer
func (d *DB) Close() error { return errors.Join(d.reads.Close(), d.sql.Close()) }

// Backs up databases from the old migration runner and returns the backup path.
// Those migrations are unavailable, so startup creates a new database.
func setAsideLegacy(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", nil
	}
	probe, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return "", err
	}
	var legacy int
	err = probe.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&legacy)
	if err == nil && legacy > 0 {
		// Checkpoint the WAL before copying the database.
		probe.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	probe.Close()
	if err != nil || legacy == 0 {
		return "", err
	}
	aside := fmt.Sprintf("%s.pre-atlas.%s", path, time.Now().UTC().Format("20060102T150405"))
	if err := os.Rename(path, aside); err != nil {
		return "", err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		os.Remove(path + suffix)
	}
	return aside, nil
}

// Applies pending migrations, runs the upgrades over the migrated rows, and notes the drift left
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
	for _, up := range upgrades {
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
		if err := applyChanges(ctx, drv, conn, path, changes); err != nil {
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

// Backs up the database and applies schema changes in one transaction.
// Keep Atlas foreign-key pragmas outside the transaction so they take effect.
func applyChanges(ctx context.Context, drv migrate.Driver, conn *sql.Conn, path string, changes []schema.Change) error {
	plan, err := drv.PlanChanges(ctx, "baseline", changes)
	if err != nil {
		return err
	}
	if _, err := copyAside(ctx, conn, path, "pre-baseline"); err != nil {
		return err
	}
	var before, body, after []string
	for _, c := range plan.Changes {
		switch {
		case !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(c.Cmd)), "PRAGMA"):
			body = append(body, c.Cmd)
		case len(body) == 0:
			before = append(before, c.Cmd)
		default:
			after = append(after, c.Cmd)
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
		run(after)
		return err
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

// Copies the embedded migration files into an atlas memory dir
func migrationDir() (*migrate.MemDir, error) {
	dir := &migrate.MemDir{}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
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
	mem, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer mem.Close()
	mem.SetMaxOpenConns(1)
	for _, stmt := range stmts {
		if _, err := mem.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("replay schema.sql %q: %w", stmt, err)
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
