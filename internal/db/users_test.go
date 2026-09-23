package db

import (
	"context"
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
	if ok, err := d.DeleteUser(ctx, "ann"); err != nil || !ok {
		t.Fatal("delete", ok, err)
	}
	if ok, _ := d.DeleteUser(ctx, "ann"); ok {
		t.Fatal("delete twice")
	}
}

func TestApiTokensTable(t *testing.T) {
	d, _ := open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ann := &v1.User{Id: "u1", Username: "ann", CreatedAt: timestamppb.New(now), UpdatedAt: timestamppb.New(now)}
	if err := d.PutUser(ctx, ann, "hash"); err != nil {
		t.Fatal(err)
	}
	first := TokenRow{Token: &v1.ApiToken{Id: "t1", Name: "laptop", Secret: "s1", CreatedAt: timestamppb.New(now)}, Provider: LocalProvider, Subject: "u1", Owner: "ann"}
	second := TokenRow{Token: &v1.ApiToken{Id: "t2", Name: "ci", Secret: "s2", CreatedAt: timestamppb.New(now.Add(time.Second))}, Provider: LocalProvider, Subject: "u1", Owner: "ann"}
	external := TokenRow{Token: &v1.ApiToken{Id: "t3", Name: "sso", Secret: "s3", CreatedAt: timestamppb.New(now)}, Provider: "oidc", Subject: "ext", Owner: "Someone", Email: "someone@example.com"}
	for _, r := range []TokenRow{first, second, external} {
		if err := d.PutToken(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.PutToken(ctx, TokenRow{Token: &v1.ApiToken{Id: "t4", Name: "dup", Secret: "s1", CreatedAt: timestamppb.New(now)}, Provider: LocalProvider, Subject: "u1", Owner: "ann"}); err == nil {
		t.Fatal("duplicate secret should fail")
	}
	got, err := d.FindToken(ctx, "s3")
	if err != nil || got.Provider != "oidc" || got.Subject != "ext" || got.Email != "someone@example.com" || got.Token.GetName() != "sso" || got.Token.GetLastUsedAt() != nil {
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
	if ok, _ := d.DeleteToken(ctx, "oidc", "ext", "t1"); ok {
		t.Fatal("another identity's token should not be deletable")
	}
	if ok, err := d.DeleteToken(ctx, LocalProvider, "u1", "t1"); err != nil || !ok {
		t.Fatal("delete", ok, err)
	}
	// Removing the account removes its tokens and nobody else's
	if ok, err := d.DeleteUser(ctx, "ann"); err != nil || !ok {
		t.Fatal("delete user", ok, err)
	}
	if _, err := d.FindToken(ctx, "s2"); !IsNotFound(err) {
		t.Fatalf("token after account removal %v", err)
	}
	if _, err := d.FindToken(ctx, "s3"); err != nil {
		t.Fatalf("sso token after account removal %v", err)
	}
}
