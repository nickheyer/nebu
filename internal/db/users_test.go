package db

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestUsersTable(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	u := &v1.User{Id: "u1", Username: "ann", CreatedAt: timestamppb.New(now), UpdatedAt: timestamppb.New(now)}
	if err := d.PutUser(ctx, u, "hash1"); err != nil {
		t.Fatal(err)
	}
	if err := d.PutUser(ctx, &v1.User{Id: "u2", Username: "ann", CreatedAt: timestamppb.New(now), UpdatedAt: timestamppb.New(now)}, "hash2"); err == nil {
		t.Fatal("duplicate username should fail")
	}
	row, err := d.GetUser(ctx, "ann")
	if err != nil || row.Hash != "hash1" || row.User.GetId() != "u1" || !row.User.GetCreatedAt().AsTime().Equal(now) {
		t.Fatalf("get %v %v", row, err)
	}
	if _, err := d.GetUser(ctx, "bob"); !IsNotFound(err) {
		t.Fatalf("missing user %v", err)
	}
	later := now.Add(time.Minute)
	if ok, err := d.SetUserPassword(ctx, "ann", "hash3", later); err != nil || !ok {
		t.Fatal("set password", ok, err)
	}
	if ok, _ := d.SetUserPassword(ctx, "bob", "x", later); ok {
		t.Fatal("password for missing user")
	}
	row, _ = d.GetUser(ctx, "ann")
	if row.Hash != "hash3" || !row.User.GetUpdatedAt().AsTime().Equal(later) {
		t.Fatalf("after change %v", row)
	}
	list, _ := d.ListUsers(ctx)
	if len(list) != 1 {
		t.Fatalf("list %v", list)
	}
	if id, err := d.DeleteUser(ctx, "ann"); err != nil || id == "" {
		t.Fatal("delete", id, err)
	}
	if id, _ := d.DeleteUser(ctx, "ann"); id != "" {
		t.Fatal("delete twice")
	}
}

func TestApiTokensTable(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ann := &v1.User{Id: "u1", Username: "ann", Role: v1.Role_ROLE_OPERATOR, CreatedAt: timestamppb.New(now), UpdatedAt: timestamppb.New(now)}
	if err := d.PutUser(ctx, ann, "hash"); err != nil {
		t.Fatal(err)
	}
	if row, err := d.GetUser(ctx, "ann"); err != nil || row.User.GetRole() != v1.Role_ROLE_OPERATOR {
		t.Fatalf("role round trip %v %v", row.User, err)
	}
	read := []v1.Scope{v1.Scope_SCOPE_READ, v1.Scope_SCOPE_INFERENCE, v1.Scope_SCOPE_READ}
	first := TokenRow{Token: &v1.ApiToken{Id: "t1", Name: "laptop", Prefix: "p1", Scopes: read, CreatedAt: timestamppb.New(now)}, Hash: TokenHash("s1"), Role: v1.Role_ROLE_OPERATOR, Provider: LocalProvider, Subject: "u1", Owner: "ann"}
	second := TokenRow{Token: &v1.ApiToken{Id: "t2", Name: "ci", Prefix: "p2", CreatedAt: timestamppb.New(now.Add(time.Second))}, Hash: TokenHash("s2"), Role: v1.Role_ROLE_OPERATOR, Provider: LocalProvider, Subject: "u1", Owner: "ann"}
	external := TokenRow{Token: &v1.ApiToken{Id: "t3", Name: "sso", Prefix: "p3", Scopes: []v1.Scope{v1.Scope_SCOPE_ADMIN}, CreatedAt: timestamppb.New(now)}, Hash: TokenHash("s3"), Role: v1.Role_ROLE_ADMIN, Provider: "oidc", Subject: "ext", Owner: "Someone", Email: "someone@example.com"}
	for _, r := range []TokenRow{first, second, external} {
		if err := d.PutToken(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.PutToken(ctx, TokenRow{Token: &v1.ApiToken{Id: "t4", Name: "dup", Prefix: "p1", CreatedAt: timestamppb.New(now)}, Hash: TokenHash("s4"), Provider: LocalProvider, Subject: "u1", Owner: "ann"}); err == nil {
		t.Fatal("duplicate prefix should fail")
	}
	got, err := d.FindToken(ctx, "p3")
	if err != nil || got.Provider != "oidc" || got.Subject != "ext" || got.Email != "someone@example.com" || got.Token.GetName() != "sso" || got.Token.GetLastUsedAt() != nil ||
		got.Hash != TokenHash("s3") || got.Role != v1.Role_ROLE_ADMIN || len(got.Token.GetScopes()) != 1 || got.Token.GetScopes()[0] != v1.Scope_SCOPE_ADMIN || got.Token.GetSecret() != "" {
		t.Fatalf("find %v %v", got, err)
	}
	if _, err := d.FindToken(ctx, "nope"); !IsNotFound(err) {
		t.Fatalf("missing token %v", err)
	}
	used := now.Add(time.Minute)
	if err := d.TouchToken(ctx, "t1", used); err != nil {
		t.Fatal(err)
	}
	mine, _ := d.ListTokens(ctx, LocalProvider, "u1")
	if len(mine) != 2 || mine[0].Token.GetId() != "t2" || mine[1].Token.GetId() != "t1" || !mine[1].Token.GetLastUsedAt().AsTime().Equal(used) {
		t.Fatalf("list %v", mine)
	}
	if s := mine[1].Token.GetScopes(); len(s) != 2 || s[0] != v1.Scope_SCOPE_INFERENCE || s[1] != v1.Scope_SCOPE_READ || mine[1].Token.GetPrefix() != "p1" {
		t.Fatalf("scopes dedupe and sort %v", mine[1].Token)
	}
	if ok, _ := d.DeleteToken(ctx, "oidc", "ext", "t1"); ok {
		t.Fatal("another identity's token should not be deletable")
	}
	if ok, err := d.DeleteToken(ctx, LocalProvider, "u1", "t1"); err != nil || !ok {
		t.Fatal("delete", ok, err)
	}
	// Removing the account removes its tokens and nobody else's
	if id, err := d.DeleteUser(ctx, "ann"); err != nil || id == "" {
		t.Fatal("delete user", id, err)
	}
	if _, err := d.FindToken(ctx, "p2"); !IsNotFound(err) {
		t.Fatalf("token after account removal %v", err)
	}
	if _, err := d.FindToken(ctx, "p3"); err != nil {
		t.Fatalf("sso token after account removal %v", err)
	}
}

func TestCreateFirstUserRace(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	const racers = 8
	errs := make(chan error, racers)
	for i := range racers {
		go func() {
			u := &v1.User{Id: fmt.Sprintf("u%d", i), Username: fmt.Sprintf("user%d", i), Role: v1.Role_ROLE_ADMIN, CreatedAt: timestamppb.New(now), UpdatedAt: timestamppb.New(now)}
			errs <- d.CreateFirstUser(ctx, u, "hash")
		}()
	}
	won := 0
	for range racers {
		switch err := <-errs; {
		case err == nil:
			won++
		case !errors.Is(err, ErrAccountsExist):
			t.Fatalf("racer %v", err)
		}
	}
	list, _ := d.ListUsers(ctx)
	if won != 1 || len(list) != 1 {
		t.Fatalf("%d racers won, %d accounts", won, len(list))
	}
}

// Plaintext tokens and role-less accounts from before hashing become hashed admin rows
func TestLegacyTokensHashedInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nebu.db")
	old := openBefore(t, path, "20261103061530_hashed_tokens_roles")
	legacy := "Zk3x9_QmA-bc8yTq2LwPpR0sUvNnJ4hEoDgFiKa7XYz"
	for _, stmt := range []string{
		`INSERT INTO users (id, username, password_hash, created_at, updated_at) VALUES ('u1', 'ann', 'h', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`INSERT INTO api_tokens (id, provider, subject, owner, email, name, secret, created_at) VALUES ('t1', 'local', 'u1', 'ann', '', 'laptop', '` + legacy + `', '2026-01-01T00:00:00Z')`,
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
	if len(d.Drift) != 0 {
		t.Fatalf("drift after migrating: %v", d.Drift)
	}
	ctx := context.Background()
	row, err := d.FindToken(ctx, legacy[:12])
	if err != nil {
		t.Fatal(err)
	}
	if row.Hash != TokenHash(legacy) || row.Role != v1.Role_ROLE_ADMIN || len(row.Token.GetScopes()) != 4 || row.Token.GetName() != "laptop" {
		t.Fatalf("legacy row %+v", row)
	}
	user, err := d.GetUser(ctx, "ann")
	if err != nil || user.User.GetRole() != v1.Role_ROLE_ADMIN {
		t.Fatalf("legacy account %v %v", user.User, err)
	}
	// A second open leaves hashed rows as they are
	d.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if row, err := again.FindToken(ctx, legacy[:12]); err != nil || row.Hash != TokenHash(legacy) {
		t.Fatalf("rehashed %v %v", row, err)
	}
}
