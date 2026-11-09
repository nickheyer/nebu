package services

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
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
	tokens := auth.NewTokens(store, log)
	users := auth.NewUsers(store, log, tokens)
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
	daemon := func(msg *v1.CreateUserRequest) *connect.Request[v1.CreateUserRequest] {
		return withBearer(msg, "daemon-token")
	}
	if _, err := svc.CreateUser(ctx, connect.NewRequest(&v1.CreateUserRequest{Username: "ann", Password: "ann-password"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("anonymous create %v", err)
	}
	if _, err := svc.CreateUser(ctx, daemon(&v1.CreateUserRequest{Username: "ann", Password: "ann-password"})); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateUser(ctx, daemon(&v1.CreateUserRequest{Username: "ann", Password: "ann-password", Role: v1.Role_ROLE_ADMIN})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("duplicate %v", err)
	}
	if _, err := svc.CreateUser(ctx, daemon(&v1.CreateUserRequest{Username: "bob", Password: "bob-password"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("later account without a role %v", err)
	}
	if _, err := svc.CreateUser(ctx, daemon(&v1.CreateUserRequest{Username: "bob", Password: "bob-password", Role: v1.Role_ROLE_OPERATOR})); err != nil {
		t.Fatal(err)
	}
	ann, _ := users.Verify(ctx, "ann", "ann-password")
	bob, _ := users.Verify(ctx, "bob", "bob-password")
	if ann.GetRole() != v1.Role_ROLE_ADMIN || bob.GetRole() != v1.Role_ROLE_OPERATOR {
		t.Fatalf("roles %v %v", ann.GetRole(), bob.GetRole())
	}
	cookie, _ := sessions.Cookie(auth.Session{Subject: ann.GetId(), Name: "ann", Provider: auth.ProviderLocal, Role: v1.Role_ROLE_ADMIN, Issued: time.Now(), Expires: time.Now().Add(time.Hour)})
	bobCookie, _ := sessions.Cookie(auth.Session{Subject: bob.GetId(), Name: "bob", Provider: auth.ProviderLocal, Role: v1.Role_ROLE_OPERATOR, Issued: time.Now(), Expires: time.Now().Add(time.Hour)})
	// A session must prove its own current password, even an admin changing another account
	if _, err := svc.SetPassword(ctx, withCookie(&v1.SetPasswordRequest{Username: "bob", Password: "bob-newpass"}, cookie)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("no current password %v", err)
	}
	if _, err := svc.SetPassword(ctx, withCookie(&v1.SetPasswordRequest{Username: "bob", Password: "bob-newpass", CurrentPassword: "ann-password"}, cookie)); err != nil {
		t.Fatalf("with current password %v", err)
	}
	if _, err := users.Verify(ctx, "bob", "bob-newpass"); err != nil {
		t.Fatal("bob's password changed")
	}
	// An operator changes its own password and nobody else's
	if _, err := svc.SetPassword(ctx, withCookie(&v1.SetPasswordRequest{Username: "ann", Password: "ann-stolen-pass", CurrentPassword: "bob-newpass"}, bobCookie)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("operator changing an admin's password %v", err)
	}
	// The daemon token changes any password without one
	if _, err := svc.SetPassword(ctx, withBearer(&v1.SetPasswordRequest{Username: "ann", Password: "ann-newpass"}, "daemon-token")); err != nil {
		t.Fatalf("token reset %v", err)
	}
	if _, err := svc.SetPassword(ctx, withBearer(&v1.SetPasswordRequest{Username: "ghost", Password: "ghost-pass"}, "daemon-token")); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown %v", err)
	}
	// Admins cannot remove themselves, operators remove nobody, the last account stays
	cookie, _ = sessions.Cookie(auth.Session{Subject: ann.GetId(), Name: "ann", Provider: auth.ProviderLocal, Role: v1.Role_ROLE_ADMIN, Issued: time.Now().Add(time.Second), Expires: time.Now().Add(time.Hour)})
	if _, err := svc.DeleteUser(ctx, withCookie(&v1.DeleteUserRequest{Username: "Ann"}, cookie)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("self delete %v", err)
	}
	if _, err := svc.DeleteUser(ctx, withBearer(&v1.DeleteUserRequest{Username: "bob"}, "daemon-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteUser(ctx, withBearer(&v1.DeleteUserRequest{Username: "ann"}, "daemon-token")); connect.CodeOf(err) != connect.CodeFailedPrecondition {
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
	for _, u := range []struct {
		name string
		role v1.Role
	}{{"ann", v1.Role_ROLE_ADMIN}, {"bob", v1.Role_ROLE_OPERATOR}} {
		if _, err := svc.CreateUser(ctx, withBearer(&v1.CreateUserRequest{Username: u.name, Password: u.name + "-password", Role: u.role}, "daemon-token")); err != nil {
			t.Fatal(err)
		}
	}
	ann, _ := users.Verify(ctx, "ann", "ann-password")
	bob, _ := users.Verify(ctx, "bob", "bob-password")
	annCookie, _ := sessions.Cookie(auth.Session{Subject: ann.GetId(), Name: "ann", Provider: auth.ProviderLocal, Role: v1.Role_ROLE_ADMIN, Issued: time.Now(), Expires: time.Now().Add(time.Hour)})
	bobCookie, _ := sessions.Cookie(auth.Session{Subject: bob.GetId(), Name: "bob", Provider: auth.ProviderLocal, Role: v1.Role_ROLE_OPERATOR, Issued: time.Now(), Expires: time.Now().Add(time.Hour)})
	all := []v1.Scope{v1.Scope_SCOPE_INFERENCE, v1.Scope_SCOPE_READ, v1.Scope_SCOPE_OPERATE, v1.Scope_SCOPE_ADMIN}
	// The daemon token and anonymous calls have no account to hold tokens
	if _, err := svc.CreateToken(ctx, withBearer(&v1.CreateTokenRequest{Name: "x", Scopes: all}, "daemon-token")); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("daemon token %v", err)
	}
	if _, err := svc.ListTokens(ctx, connect.NewRequest(&v1.ListTokensRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("anonymous %v", err)
	}
	if _, err := svc.CreateToken(ctx, withCookie(&v1.CreateTokenRequest{Name: "  ", Scopes: all}, annCookie)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("blank name %v", err)
	}
	if _, err := svc.CreateToken(ctx, withCookie(&v1.CreateTokenRequest{Name: "admin", Scopes: all}, bobCookie)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("operator granting admin %v", err)
	}
	made, err := svc.CreateToken(ctx, withCookie(&v1.CreateTokenRequest{Name: "laptop", Scopes: all}, annCookie))
	if err != nil {
		t.Fatal(err)
	}
	tok := made.Msg.GetToken()
	if !strings.HasPrefix(tok.GetSecret(), "nebu_"+tok.GetPrefix()) || tok.GetName() != "laptop" || tok.GetCreatedAt() == nil || tok.GetLastUsedAt() != nil || len(tok.GetScopes()) != 4 {
		t.Fatalf("made %v", tok)
	}
	// The token authenticates as ann and can manage her tokens, including making more
	if who, ok := guard.Identify(http.Header{"Authorization": {"Bearer " + tok.GetSecret()}}); !ok || who.Name != "ann" || who.Subject != ann.GetId() || who.Provider != auth.ProviderLocal || who.TokenPrefix != tok.GetPrefix() {
		t.Fatalf("identify %v %v", who, ok)
	}
	ci, err := svc.CreateToken(ctx, withBearer(&v1.CreateTokenRequest{Name: "ci", Scopes: []v1.Scope{v1.Scope_SCOPE_INFERENCE}}, tok.GetSecret()))
	if err != nil {
		t.Fatalf("token makes token %v", err)
	}
	listed, _ := svc.ListTokens(ctx, withBearer(&v1.ListTokensRequest{}, tok.GetSecret()))
	if got := listed.Msg.GetTokens(); len(got) != 2 || got[0].GetName() != "ci" || got[1].GetId() != tok.GetId() || got[1].GetLastUsedAt() == nil || got[0].GetSecret() != "" || got[1].GetSecret() != "" {
		t.Fatalf("ann's tokens %v", got)
	}
	// An inference token cannot mint one holding more
	if _, err := svc.CreateToken(ctx, withBearer(&v1.CreateTokenRequest{Name: "wider", Scopes: []v1.Scope{v1.Scope_SCOPE_READ}}, ci.Msg.GetToken().GetSecret())); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("inference token widening %v", err)
	}
	// Bob sees none of ann's and cannot revoke hers
	listed, _ = svc.ListTokens(ctx, withCookie(&v1.ListTokensRequest{}, bobCookie))
	if len(listed.Msg.GetTokens()) != 0 {
		t.Fatalf("bob's tokens %v", listed.Msg.GetTokens())
	}
	if _, err := svc.DeleteToken(ctx, withCookie(&v1.DeleteTokenRequest{Id: tok.GetId()}, bobCookie)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("bob revoking ann's %v", err)
	}
	// A token of a local account proves the account's own password, like a cookie
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
	if _, ok := guard.Identify(http.Header{"Authorization": {"Bearer " + tok.GetSecret()}}); ok {
		t.Fatal("revoked token still authenticates")
	}
	rest := ci.Msg.GetToken().GetSecret()
	if _, ok := guard.Identify(http.Header{"X-Api-Key": {rest}}); !ok {
		t.Fatal("the remaining token should pass as an x-api-key too")
	}
	if _, err := svc.DeleteUser(ctx, withBearer(&v1.DeleteUserRequest{Username: "ann"}, "daemon-token")); err != nil {
		t.Fatal(err)
	}
	if _, ok := guard.Identify(http.Header{"Authorization": {"Bearer " + rest}}); ok {
		t.Fatal("a removed account's token still authenticates")
	}
}
