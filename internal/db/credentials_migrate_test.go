package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// The credential migrations carry stored rows over: single sign-on owners move under their provider and no other owner
// moves, a budget's renamed unit reads back under its new name and by its number in a release naming it otherwise, a
// rolling span stays as its owner wrote it, the database is copied aside first, and the down puts every owner back
func TestCredentialMigrationsCarryStoredRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nebu.db")
	old := openBefore(t, path, "20261124045123_credential_owners_units_revocations")
	budgets := `{"budgets":[{"name":"plan","unit":"BUDGET_UNIT_PERCENT_LEFT","limit":80,"window":"tokens"}, {"name":"BUDGET_UNIT_PERCENT_LEFT","unit":"BUDGET_UNIT_TOKENS","limit":5,"period":"BUDGET_PERIOD_ROLLING","seconds":60}]}`
	for _, stmt := range []string{
		`INSERT INTO users (id, username, password_hash, created_at, updated_at) VALUES ('u1', 'ann', 'x', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`,
		`INSERT INTO sources (id, kind, created_at, updated_at) VALUES ('lab', 'openai_compatible', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`,
		`INSERT INTO provider_credentials (id, owner, source_id, kind, secret, position, created_at, updated_at) VALUES ('c-shared', 'daemon', 'lab', 'key', 'k', 1, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`,
		`INSERT INTO provider_credentials (id, owner, source_id, kind, secret, position, created_at, updated_at, budgets) VALUES ('c-local', 'u1', 'lab', 'key', 'k', 1, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z', '` + budgets + `')`,
		`INSERT INTO provider_credentials (id, owner, source_id, kind, secret, position, created_at, updated_at) VALUES ('c-sso', 'auth0|abc', 'lab', 'key', 'k', 1, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`,
		`INSERT INTO provider_credentials (id, owner, source_id, kind, secret, position, created_at, updated_at) VALUES ('c-odd', 'oidc:odd', 'lab', 'key', 'k', 1, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`,
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
	owners := func() map[string]string {
		t.Helper()
		out, err := d.stringMap(ctx, `SELECT id, owner FROM provider_credentials`)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := owners(); got["c-shared"] != "daemon" || got["c-local"] != "u1" || got["c-sso"] != "oidc:auth0|abc" || got["c-odd"] != "oidc:oidc:odd" {
		t.Fatalf("owners after migrating %v", got)
	}
	rows, err := d.SourceCredentials(ctx, "u1", "lab")
	if err != nil || len(rows) != 1 || len(rows[0].Credential.GetBudgets()) != 2 {
		t.Fatalf("the local account's credential %v %v", rows, err)
	}
	plan, rolling := rows[0].Credential.GetBudgets()[0], rows[0].Credential.GetBudgets()[1]
	if plan.GetName() != "plan" || plan.GetUnit() != v1.BudgetUnit_BUDGET_UNIT_PERCENT_USED || plan.GetLimit() != 80 || plan.GetWindow() != "tokens" {
		t.Fatalf("the percent budget after migrating %v", plan)
	}
	if rolling.GetName() != "BUDGET_UNIT_PERCENT_LEFT" || rolling.GetUnit() != v1.BudgetUnit_BUDGET_UNIT_TOKENS || rolling.GetSeconds() != 60 || rolling.GetPeriod() != v1.BudgetPeriod_BUDGET_PERIOD_ROLLING {
		t.Fatalf("a budget only named like the old unit, its span as written, after migrating %v", rolling)
	}
	raw := func() string {
		t.Helper()
		var col string
		if err := d.reads.QueryRowContext(ctx, `SELECT budgets FROM provider_credentials WHERE id = 'c-local'`).Scan(&col); err != nil {
			t.Fatal(err)
		}
		return col
	}
	// Whatever a release calls value 5 of the unit, it reads the number
	byNumber := &v1.ProviderCredential{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(raw()), byNumber); err != nil || int32(byNumber.GetBudgets()[0].GetUnit()) != 5 {
		t.Fatalf("the stored unit %s %v", raw(), err)
	}
	if strings.Contains(raw(), `"unit":"BUDGET_UNIT_PERCENT`) {
		t.Fatalf("the unit is stored by a name a release before or after may not know: %s", raw())
	}
	if ok, err := d.EditCredential(ctx, "u1", "c-local", "", "", rows[0].Credential.GetBudgets(), true, time.Now()); err != nil || !ok {
		t.Fatalf("edit %v %v", ok, err)
	}
	if strings.Contains(raw(), `"unit":"`) || strings.Contains(raw(), `"period":"`) {
		t.Fatalf("budgets written now name an enum a release before may not know: %s", raw())
	}

	// The copy made before the migrations ran holds the rows as they were
	if d.Backup == "" || !strings.Contains(d.Backup, beforeMigrating) {
		t.Fatalf("no copy was made before migrating: %q", d.Backup)
	}
	if info, err := os.Stat(d.Backup); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the copy %v %v", info, err)
	}
	before, err := sql.Open("sqlite", "file:"+d.Backup+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	var owner, kept string
	if err := before.QueryRowContext(ctx, `SELECT owner FROM provider_credentials WHERE id = 'c-sso'`).Scan(&owner); err != nil || owner != "auth0|abc" {
		t.Fatalf("the copy's owner %q %v", owner, err)
	}
	if err := before.QueryRowContext(ctx, `SELECT budgets FROM provider_credentials WHERE id = 'c-local'`).Scan(&kept); err != nil || kept != budgets {
		t.Fatalf("the copy's budgets %q %v", kept, err)
	}

	if err := d.MigrateDown(ctx, "20261120080432"); err != nil {
		t.Fatal(err)
	}
	if got := owners(); got["c-shared"] != "daemon" || got["c-local"] != "u1" || got["c-sso"] != "auth0|abc" || got["c-odd"] != "oidc:odd" {
		t.Fatalf("owners after the down %v", got)
	}
}

// A database opened with nothing pending is not copied, and only the newest copies made before migrations are kept
func TestBackupsBeforeMigratingAreFewAndOnlyWhenPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nebu.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Backup != "" {
		t.Fatalf("a new database was copied aside: %s", d.Backup)
	}
	d.Close()
	if d, err = Open(path); err != nil || d.Backup != "" {
		t.Fatalf("a database with nothing pending was copied aside: %v %v", d, err)
	}
	ctx := context.Background()
	for i, stale := range []string{"20200101T000000", "20200102T000000", "20200103T000000", "20200104T000000"} {
		if err := os.WriteFile(path+"."+beforeMigrating+"."+stale, []byte{byte(i)}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The last migration is taken back, so the next open has one pending
	files := migrationFilesList(t)
	if err := d.MigrateDown(ctx, files[len(files)-2].Version()); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if d, err = Open(path); err != nil || d.Backup == "" {
		t.Fatalf("a database with a migration pending was not copied aside: %v %v", d, err)
	}
	defer d.Close()
	copies, _ := filepath.Glob(path + "." + beforeMigrating + ".*")
	if len(copies) != backupsKept {
		t.Fatalf("copies kept %v", copies)
	}
	for _, c := range copies {
		if strings.HasSuffix(c, "20200101T000000") || strings.HasSuffix(c, "20200102T000000") {
			t.Fatalf("an old copy outlived newer ones: %v", copies)
		}
	}
}

// A database whose mode cannot be set opens all the same, the reason answered, and one that can is made owner-only
func TestOwnerOnlyNeverKeepsTheDatabaseShut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nebu.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if exposed, err := ownerOnly(path); err != nil || exposed != "" {
		t.Fatalf("owner only %q %v", exposed, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info, err)
	}
	if exposed, err := ownerOnly(":memory:"); err != nil || exposed != "" {
		t.Fatalf("memory %q %v", exposed, err)
	}
}

// A removed sign-in's token is queued for revocation in the write removing it and stays queued until dropped
func TestRevocationsQueueWithTheRemoval(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if _, err := d.sql.ExecContext(ctx, `INSERT INTO sources (id, kind, created_at, updated_at) VALUES ('lab', 'openai', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.ExecContext(ctx, `INSERT INTO provider_credentials (id, owner, source_id, kind, secret, position, created_at, updated_at) VALUES ('c1', 'ann', 'lab', 'sign_in', '{}', 1, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	rev := &Revocation{ID: "c1", Endpoint: "https://auth.example/revoke", ClientID: "app", Token: "refresh", Since: now}
	if ok, err := d.DeleteCredential(ctx, "bob", "c1", rev); err != nil || ok {
		t.Fatalf("another owner removed it: %v %v", ok, err)
	}
	if list, err := d.Revocations(ctx); err != nil || len(list) != 0 {
		t.Fatalf("a removal that removed nothing queued a revocation: %v %v", list, err)
	}
	if ok, err := d.DeleteCredential(ctx, "ann", "c1", rev); err != nil || !ok {
		t.Fatalf("remove %v %v", ok, err)
	}
	if err := d.TriedRevocation(ctx, "c1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	list, err := d.Revocations(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("queued %v %v", list, err)
	}
	if r := list[0]; r.ID != "c1" || r.Endpoint != rev.Endpoint || r.ClientID != "app" || r.Token != "refresh" || !r.Since.Equal(now) || !r.Tried.Equal(now.Add(time.Minute)) || r.Tries != 1 {
		t.Fatalf("queued %+v", r)
	}
	if err := d.DropRevocation(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if list, err := d.Revocations(ctx); err != nil || len(list) != 0 {
		t.Fatalf("after the drop %v %v", list, err)
	}
}
