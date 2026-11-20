package services

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/auth"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

var _ nebuv1connect.AuthServiceHandler = (*AuthService)(nil)

var (
	errAccountsOff = connect.NewError(connect.CodeUnimplemented, errors.New("local accounts are off, auth.oidc or auth.disabled is set"))
	errTokensOff   = connect.NewError(connect.CodeUnimplemented, errors.New("API tokens are off, auth.disabled is set"))
)

// Manages local accounts, the API tokens signed-in users make and the credentials callers keep, the auth package deciding who may do what
type AuthService struct {
	users       *auth.Users
	tokens      *auth.Tokens
	credentials *auth.Credentials
	guard       *auth.Guard
}

// Nil users means accounts are off, nil tokens means auth is off, and credentials are kept either way
func NewAuthService(users *auth.Users, tokens *auth.Tokens, credentials *auth.Credentials, guard *auth.Guard) *AuthService {
	return &AuthService{users: users, tokens: tokens, credentials: credentials, guard: guard}
}

func (s *AuthService) ListUsers(ctx context.Context, req *connect.Request[v1.ListUsersRequest]) (*connect.Response[v1.ListUsersResponse], error) {
	if s.users == nil {
		return nil, errAccountsOff
	}
	list, err := s.users.List(ctx)
	if err != nil {
		return authReply(&v1.ListUsersResponse{}, err)
	}
	list = match(list, req.Msg.GetQuery(), func(u *v1.User) []string { return []string{u.GetUsername()} })
	list, next, err := page(list, req.Msg.GetCursor(), req.Msg.GetLimit())
	return authReply(&v1.ListUsersResponse{Users: list, NextCursor: next}, err)
}

func (s *AuthService) CreateUser(ctx context.Context, req *connect.Request[v1.CreateUserRequest]) (*connect.Response[v1.CreateUserResponse], error) {
	if s.users == nil {
		return nil, errAccountsOff
	}
	u, err := s.users.Create(ctx, s.caller(req.Header()), req.Msg.GetUsername(), req.Msg.GetPassword(), req.Msg.GetRole())
	return authReply(&v1.CreateUserResponse{User: u}, err)
}

func (s *AuthService) DeleteUser(ctx context.Context, req *connect.Request[v1.DeleteUserRequest]) (*connect.Response[v1.DeleteUserResponse], error) {
	if s.users == nil {
		return nil, errAccountsOff
	}
	return authReply(&v1.DeleteUserResponse{}, s.users.Delete(ctx, s.caller(req.Header()), req.Msg.GetUsername()))
}

func (s *AuthService) SetPassword(ctx context.Context, req *connect.Request[v1.SetPasswordRequest]) (*connect.Response[v1.SetPasswordResponse], error) {
	if s.users == nil {
		return nil, errAccountsOff
	}
	u, err := s.users.SetPassword(ctx, s.caller(req.Header()), req.Msg.GetUsername(), req.Msg.GetCurrentPassword(), req.Msg.GetPassword())
	return authReply(&v1.SetPasswordResponse{User: u}, err)
}

func (s *AuthService) SetRole(ctx context.Context, req *connect.Request[v1.SetRoleRequest]) (*connect.Response[v1.SetRoleResponse], error) {
	if s.users == nil {
		return nil, errAccountsOff
	}
	u, err := s.users.SetRole(ctx, s.caller(req.Header()), req.Msg.GetUsername(), req.Msg.GetRole())
	return authReply(&v1.SetRoleResponse{User: u}, err)
}

func (s *AuthService) ListTokens(ctx context.Context, req *connect.Request[v1.ListTokensRequest]) (*connect.Response[v1.ListTokensResponse], error) {
	if s.tokens == nil {
		return nil, errTokensOff
	}
	list, err := s.tokens.List(ctx, s.caller(req.Header()))
	if err != nil {
		return authReply(&v1.ListTokensResponse{}, err)
	}
	list = match(list, req.Msg.GetQuery(), func(t *v1.ApiToken) []string { return []string{t.GetName(), t.GetPrefix()} })
	list, next, err := page(list, req.Msg.GetCursor(), req.Msg.GetLimit())
	return authReply(&v1.ListTokensResponse{Tokens: list, NextCursor: next}, err)
}

func (s *AuthService) CreateToken(ctx context.Context, req *connect.Request[v1.CreateTokenRequest]) (*connect.Response[v1.CreateTokenResponse], error) {
	if s.tokens == nil {
		return nil, errTokensOff
	}
	tok, err := s.tokens.Create(ctx, s.caller(req.Header()), req.Msg.GetName(), req.Msg.GetScopes())
	return authReply(&v1.CreateTokenResponse{Token: tok}, err)
}

func (s *AuthService) DeleteToken(ctx context.Context, req *connect.Request[v1.DeleteTokenRequest]) (*connect.Response[v1.DeleteTokenResponse], error) {
	if s.tokens == nil {
		return nil, errTokensOff
	}
	return authReply(&v1.DeleteTokenResponse{}, s.tokens.Delete(ctx, s.caller(req.Header()), req.Msg.GetId()))
}

func (s *AuthService) ListCredentials(ctx context.Context, req *connect.Request[v1.ListCredentialsRequest]) (*connect.Response[v1.ListCredentialsResponse], error) {
	list, err := s.credentials.List(ctx, s.owner(req.Header()))
	if err != nil {
		return authReply(&v1.ListCredentialsResponse{}, err)
	}
	list = match(list, req.Msg.GetQuery(), func(c *v1.ProviderCredential) []string { return []string{c.GetName(), c.GetSourceId()} })
	list, next, err := page(list, req.Msg.GetCursor(), req.Msg.GetLimit())
	return authReply(&v1.ListCredentialsResponse{Credentials: list, NextCursor: next}, err)
}

func (s *AuthService) PutCredential(ctx context.Context, req *connect.Request[v1.PutCredentialRequest]) (*connect.Response[v1.PutCredentialResponse], error) {
	c, err := s.credentials.Put(ctx, s.owner(req.Header()), req.Msg)
	return authReply(&v1.PutCredentialResponse{Credential: c}, err)
}

func (s *AuthService) DeleteCredential(ctx context.Context, req *connect.Request[v1.DeleteCredentialRequest]) (*connect.Response[v1.DeleteCredentialResponse], error) {
	return authReply(&v1.DeleteCredentialResponse{}, s.credentials.Delete(ctx, s.owner(req.Header()), req.Msg.GetId()))
}

func (s *AuthService) StartSignIn(ctx context.Context, req *connect.Request[v1.StartSignInRequest]) (*connect.Response[v1.StartSignInResponse], error) {
	in, err := s.credentials.StartSignIn(ctx, s.owner(req.Header()), req.Msg.GetSourceId(), req.Msg.GetName())
	return authReply(&v1.StartSignInResponse{SignIn: in}, err)
}

func (s *AuthService) FinishSignIn(ctx context.Context, req *connect.Request[v1.FinishSignInRequest]) (*connect.Response[v1.FinishSignInResponse], error) {
	c, err := s.credentials.FinishSignIn(ctx, s.owner(req.Header()), req.Msg.GetId(), req.Msg.GetCallback())
	return authReply(&v1.FinishSignInResponse{Credential: c}, err)
}

// The owner the caller's provider credentials are kept under
func (s *AuthService) owner(h http.Header) string { return auth.CredentialOwner(s.caller(h)) }

// Who is behind the request, the zero identity when nothing valid was sent
func (s *AuthService) caller(h http.Header) auth.Identity {
	id, _ := s.guard.Identify(h)
	return id
}

// Maps the auth package's refusals onto their codes, everything else as reply does
func authReply[R any](msg *R, err error) (*connect.Response[R], error) {
	switch {
	case errors.Is(err, auth.ErrForbidden):
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, auth.ErrNoAccount), errors.Is(err, auth.ErrOwnAccount), errors.Is(err, auth.ErrLastAdmin):
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return reply(msg, err)
}
