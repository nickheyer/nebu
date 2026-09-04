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

// The schema atlas wrote the migrations from, what the live database is held to
//
//go:embed schema.sql
var desiredSchema string

const operator = "nebu"

// Returned when a record is not in the store
var ErrNotFound = errors.New("not found")

// Open store
type DB struct {
	sql  *sql.DB
	path string
	// Where a database from before the atlas migrations was moved, empty when none was
	SetAside string
	// Ways the live schema differs from schema.sql, empty when none
	Drift []string
	// How a database whose revisions the migration directory no longer holds was brought to its head, empty when it was not
	Baselined string
}

// Opens or creates the database and brings it onto the embedded migration head
func Open(path string) (*DB, error) {
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
	d := &DB{sql: sqldb, path: path, SetAside: aside}
	if err := d.migrate(context.Background()); err != nil {
		sqldb.Close()
		return nil, err
	}
	return d, nil
}

// Closes the database
func (d *DB) Close() error { return d.sql.Close() }

// Moves a database the hand rolled runner wrote out of the way, returning where it went
//
// Its migrations no longer exist, so it cannot be brought forward, and a
// fresh database is what every nebu before release starts from.
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
		// The write ahead log folds back into the file so the copy is whole
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

// Applies pending migrations with atlas and records them in its revision table
func (d *DB) migrate(ctx context.Context) error {
	dir, err := migrationDir()
	if err != nil {
		return err
	}
	if err := migrate.Validate(dir); err != nil {
		return fmt.Errorf("migration directory: %w", err)
	}
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, revisionTableDDL); err != nil {
		return fmt.Errorf("create revision table: %w", err)
	}
	drv, err := sqlite.Open(conn)
	if err != nil {
		return err
	}
	revs := revisionStore{conn}
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
	changes, err := schemaChanges(ctx, drv)
	if err != nil {
		return err
	}
	d.Drift = describe(changes)
	return nil
}

// Brings a database the directory's revisions no longer describe onto the head in place, rows kept
//
// The init migration is rewritten until release, so a recorded version can vanish
// from the directory, and atlas would replay CREATE TABLE onto a populated file.
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

// Runs a plan for the changes as one transaction, behind a dated copy of the file
//
// A table rebuilt in place is a create, copy, drop, and rename, so the file is
// copied first and the statements commit together, the foreign key pragmas
// atlas wraps them in staying outside where they take effect.
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

// Writes the database as it stands to a dated copy beside it, the log folded in first
func copyAside(ctx context.Context, conn *sql.Conn, path, why string) (string, error) {
	if path == "" || path == ":memory:" {
		return "", nil
	}
	conn.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	aside := fmt.Sprintf("%s.%s.%s", path, why, time.Now().UTC().Format("20060102T150405"))
	return aside, os.WriteFile(aside, data, 0o600)
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

// The changes that take the live schema to schema.sql, none when they agree
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

// Puts schema changes into words, one line each
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

// Returns the applied migration versions in order
func (d *DB) Migrations(ctx context.Context) ([]string, error) {
	revs, err := revisionStore{d.sql}.ReadRevisions(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range revs {
		if r.Applied == r.Total {
			out = append(out, r.Version)
		}
	}
	return out, nil
}

func (d *DB) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
