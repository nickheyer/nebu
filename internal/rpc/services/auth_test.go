package services

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func openAuth(t *testing.T) (*auth.Users, *auth.Tokens, *auth.Sessions, *auth.Guard) {
	t.Helper()
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	users := auth.NewUsers(store, log)
	tokens := auth.NewTokens(store, log)
	sessions, _ := auth.OpenSessions(filepath.Join(dir, "k"), time.Hour)
	sessions.Validate(users.Valid)
	return users, tokens, sessions, auth.NewGuard("daemon-token", sessions, tokens)
}

func withCookie[T any](msg *T, c *http.Cookie) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Cookie", c.String())
	return req
}

func withBearer[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+token)
	return req
}

func TestAuthServiceRules(t *testing.T) {
	users, tokens, sessions, guard := openAuth(t)
	svc := NewAuthService(users, tokens, guard)
	ctx := context.Background()
	if _, err := svc.CreateUser(ctx, connect.NewRequest(&v1.CreateUserRequest{Username: "ann", Password: "ann-password"})); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateUser(ctx, connect.NewRequest(&v1.CreateUserRequest{Username: "ann", Password: "ann-password"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("duplicate %v", err)
	}
	if _, err := svc.CreateUser(ctx, connect.NewRequest(&v1.CreateUserRequest{Username: "bob", Password: "bob-password"})); err != nil {
		t.Fatal(err)
	}
	ann, _ := users.Verify(ctx, "ann", "ann-password")
	cookie, _ := sessions.Cookie(auth.Session{Subject: ann.GetId(), Name: "ann", Provider: auth.ProviderLocal, Issued: time.Now(), Expires: time.Now().Add(time.Hour)})
	// A session must prove its own current password, even to change another account
	if _, err := svc.SetPassword(ctx, withCookie(&v1.SetPasswordRequest{Username: "bob", Password: "bob-newpass"}, cookie)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("no current password %v", err)
	}
	if _, err := svc.SetPassword(ctx, withCookie(&v1.SetPasswordRequest{Username: "bob", Password: "bob-newpass", CurrentPassword: "ann-password"}, cookie)); err != nil {
		t.Fatalf("with current password %v", err)
	}
	if _, err := users.Verify(ctx, "bob", "bob-newpass"); err != nil {
		t.Fatal("bob's password changed")
	}
	// The daemon token changes any password without one
	if _, err := svc.SetPassword(ctx, withBearer(&v1.SetPasswordRequest{Username: "ann", Password: "ann-newpass"}, "daemon-token")); err != nil {
		t.Fatalf("token reset %v", err)
	}
	if _, err := svc.SetPassword(ctx, connect.NewRequest(&v1.SetPasswordRequest{Username: "ghost", Password: "ghost-pass"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown %v", err)
	}
	// Sessions cannot remove themselves, the last account stays
	cookie, _ = sessions.Cookie(auth.Session{Subject: ann.GetId(), Name: "ann", Provider: auth.ProviderLocal, Issued: time.Now().Add(time.Second), Expires: time.Now().Add(time.Hour)})
	if _, err := svc.DeleteUser(ctx, withCookie(&v1.DeleteUserRequest{Username: "Ann"}, cookie)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("self delete %v", err)
	}
	if _, err := svc.DeleteUser(ctx, connect.NewRequest(&v1.DeleteUserRequest{Username: "bob"})); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteUser(ctx, connect.NewRequest(&v1.DeleteUserRequest{Username: "ann"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("last account %v", err)
	}
	list, _ := svc.ListUsers(ctx, connect.NewRequest(&v1.ListUsersRequest{}))
	if len(list.Msg.GetUsers()) != 1 {
		t.Fatalf("list %v", list.Msg)
	}
	off := NewAuthService(nil, nil, auth.NewGuard("", nil, nil))
	if _, err := off.ListUsers(ctx, connect.NewRequest(&v1.ListUsersRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("accounts off %v", err)
	}
	if _, err := off.ListTokens(ctx, connect.NewRequest(&v1.ListTokensRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("tokens with auth off %v", err)
	}
}

func TestApiTokensBelongToTheCaller(t *testing.T) {
	users, tokens, sessions, guard := openAuth(t)
	svc := NewAuthService(users, tokens, guard)
	ctx := context.Background()
	for _, name := range []string{"ann", "bob"} {
		if _, err := svc.CreateUser(ctx, connect.NewRequest(&v1.CreateUserRequest{Username: name, Password: name + "-password"})); err != nil {
			t.Fatal(err)
		}
	}
	ann, _ := users.Verify(ctx, "ann", "ann-password")
	bob, _ := users.Verify(ctx, "bob", "bob-password")
	annCookie, _ := sessions.Cookie(auth.Session{Subject: ann.GetId(), Name: "ann", Provider: auth.ProviderLocal, Issued: time.Now(), Expires: time.Now().Add(time.Hour)})
	bobCookie, _ := sessions.Cookie(auth.Session{Subject: bob.GetId(), Name: "bob", Provider: auth.ProviderLocal, Issued: time.Now(), Expires: time.Now().Add(time.Hour)})
	// The daemon token and anonymous calls have no account to hold tokens
	if _, err := svc.CreateToken(ctx, withBearer(&v1.CreateTokenRequest{Name: "x"}, "daemon-token")); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("daemon token %v", err)
	}
	if _, err := svc.ListTokens(ctx, connect.NewRequest(&v1.ListTokensRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("anonymous %v", err)
	}
	if _, err := svc.CreateToken(ctx, withCookie(&v1.CreateTokenRequest{Name: "  "}, annCookie)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("blank name %v", err)
	}
	made, err := svc.CreateToken(ctx, withCookie(&v1.CreateTokenRequest{Name: "laptop"}, annCookie))
	if err != nil {
		t.Fatal(err)
	}
	tok := made.Msg.GetToken()
	if tok.GetSecret() == "" || tok.GetName() != "laptop" || tok.GetCreatedAt() == nil || tok.GetLastUsedAt() != nil {
		t.Fatalf("made %v", tok)
	}
	// The token authenticates as ann and can manage her tokens, including making more
	if who, ok := guard.Identify(http.Header{"Authorization": {"Bearer " + tok.GetSecret()}}); !ok || who == nil || who.Name != "ann" || who.Subject != ann.GetId() || who.Provider != auth.ProviderLocal {
		t.Fatalf("identify %v %v", who, ok)
	}
	if _, err := svc.CreateToken(ctx, withBearer(&v1.CreateTokenRequest{Name: "ci"}, tok.GetSecret())); err != nil {
		t.Fatalf("token makes token %v", err)
	}
	listed, _ := svc.ListTokens(ctx, withBearer(&v1.ListTokensRequest{}, tok.GetSecret()))
	if got := listed.Msg.GetTokens(); len(got) != 2 || got[0].GetName() != "ci" || got[1].GetId() != tok.GetId() || got[1].GetLastUsedAt() == nil {
		t.Fatalf("ann's tokens %v", got)
	}
	// Bob sees none of ann's and cannot revoke hers
	listed, _ = svc.ListTokens(ctx, withCookie(&v1.ListTokensRequest{}, bobCookie))
	if len(listed.Msg.GetTokens()) != 0 {
		t.Fatalf("bob's tokens %v", listed.Msg.GetTokens())
	}
	if _, err := svc.DeleteToken(ctx, withCookie(&v1.DeleteTokenRequest{Id: tok.GetId()}, bobCookie)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("bob revoking ann's %v", err)
	}
	// A token with a session proves the session's own password, like a cookie
	if _, err := svc.SetPassword(ctx, withBearer(&v1.SetPasswordRequest{Username: "bob", Password: "bob-newpass"}, tok.GetSecret())); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("token changing bob without ann's password %v", err)
	}
	if _, err := svc.DeleteUser(ctx, withBearer(&v1.DeleteUserRequest{Username: "ann"}, tok.GetSecret())); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("token removing its own account %v", err)
	}
	// Ann revokes one, and removing her account kills the rest
	if _, err := svc.DeleteToken(ctx, withCookie(&v1.DeleteTokenRequest{Id: tok.GetId()}, annCookie)); err != nil {
		t.Fatal(err)
	}
	if guard.Authenticated(http.Header{"Authorization": {"Bearer " + tok.GetSecret()}}) {
		t.Fatal("revoked token still authenticates")
	}
	listed, _ = svc.ListTokens(ctx, withCookie(&v1.ListTokensRequest{}, annCookie))
	rest := listed.Msg.GetTokens()[0].GetSecret()
	if !guard.Authenticated(http.Header{"X-Api-Key": {rest}}) {
		t.Fatal("the remaining token should pass as an x-api-key too")
	}
	if _, err := svc.DeleteUser(ctx, withCookie(&v1.DeleteUserRequest{Username: "ann"}, bobCookie)); err != nil {
		t.Fatal(err)
	}
	if guard.Authenticated(http.Header{"Authorization": {"Bearer " + rest}}) {
		t.Fatal("a removed account's token still authenticates")
	}
}
