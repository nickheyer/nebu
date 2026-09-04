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
	sql *sql.DB
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
	d := &DB{sql: sqldb, SetAside: aside}
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
	if d.Baselined, err = baseline(ctx, drv, dir, revs); err != nil {
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
func baseline(ctx context.Context, drv migrate.Driver, dir migrate.Dir, store revisionStore) (string, error) {
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
		if err := drv.ApplyChanges(ctx, changes); err != nil {
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
				out = append(out, fmt.Sprintf("table %s %T", c.T.Name, sub))
			}
		default:
			out = append(out, fmt.Sprintf("%T", c))
		}
	}
	return out
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
