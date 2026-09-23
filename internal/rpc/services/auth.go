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
	errNoAccount   = connect.NewError(connect.CodeFailedPrecondition, errors.New("sign in with an account to manage its API tokens, the daemon token has none"))
)

// Manages local accounts and the API tokens signed-in users make
type AuthService struct {
	users  *auth.Users
	tokens *auth.Tokens
	guard  *auth.Guard
}

// Nil users means accounts are off, nil tokens means auth is off
func NewAuthService(users *auth.Users, tokens *auth.Tokens, guard *auth.Guard) *AuthService {
	return &AuthService{users: users, tokens: tokens, guard: guard}
}

func (s *AuthService) ListUsers(ctx context.Context, req *connect.Request[v1.ListUsersRequest]) (*connect.Response[v1.ListUsersResponse], error) {
	if s.users == nil {
		return nil, errAccountsOff
	}
	list, err := s.users.List(ctx)
	return reply(&v1.ListUsersResponse{Users: list}, err)
}

func (s *AuthService) CreateUser(ctx context.Context, req *connect.Request[v1.CreateUserRequest]) (*connect.Response[v1.CreateUserResponse], error) {
	if s.users == nil {
		return nil, errAccountsOff
	}
	u, err := s.users.Create(ctx, req.Msg.GetUsername(), req.Msg.GetPassword())
	return reply(&v1.CreateUserResponse{User: u}, err)
}

// Refuses the caller's own account, so a session or token cannot strand itself
func (s *AuthService) DeleteUser(ctx context.Context, req *connect.Request[v1.DeleteUserRequest]) (*connect.Response[v1.DeleteUserResponse], error) {
	if s.users == nil {
		return nil, errAccountsOff
	}
	if me := s.account(req.Header()); me != nil && auth.SameUser(me.Name, req.Msg.GetUsername()) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("sign in as another account to remove this one"))
	}
	return reply(&v1.DeleteUserResponse{}, s.users.Delete(ctx, req.Msg.GetUsername()))
}

// Local accounts prove their own current password first. The daemon token needs none.
func (s *AuthService) SetPassword(ctx context.Context, req *connect.Request[v1.SetPasswordRequest]) (*connect.Response[v1.SetPasswordResponse], error) {
	if s.users == nil {
		return nil, errAccountsOff
	}
	if me := s.account(req.Header()); me != nil {
		if _, err := s.users.Verify(ctx, me.Name, req.Msg.GetCurrentPassword()); err != nil {
			if errors.Is(err, auth.ErrCredentials) {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("current_password must be your own current password"))
			}
			return nil, wrap(err)
		}
	}
	u, err := s.users.SetPassword(ctx, req.Msg.GetUsername(), req.Msg.GetPassword())
	return reply(&v1.SetPasswordResponse{User: u}, err)
}

func (s *AuthService) ListTokens(ctx context.Context, req *connect.Request[v1.ListTokensRequest]) (*connect.Response[v1.ListTokensResponse], error) {
	who, err := s.owner(req.Header())
	if err != nil {
		return nil, err
	}
	list, err := s.tokens.List(ctx, who)
	return reply(&v1.ListTokensResponse{Tokens: list}, err)
}

func (s *AuthService) CreateToken(ctx context.Context, req *connect.Request[v1.CreateTokenRequest]) (*connect.Response[v1.CreateTokenResponse], error) {
	who, err := s.owner(req.Header())
	if err != nil {
		return nil, err
	}
	tok, err := s.tokens.Create(ctx, who, req.Msg.GetName())
	return reply(&v1.CreateTokenResponse{Token: tok}, err)
}

func (s *AuthService) DeleteToken(ctx context.Context, req *connect.Request[v1.DeleteTokenRequest]) (*connect.Response[v1.DeleteTokenResponse], error) {
	who, err := s.owner(req.Header())
	if err != nil {
		return nil, err
	}
	return reply(&v1.DeleteTokenResponse{}, s.tokens.Delete(ctx, who, req.Msg.GetId()))
}

// The account tokens are managed for: whoever the session cookie or API token names
func (s *AuthService) owner(h http.Header) (*auth.Session, error) {
	if s.tokens == nil {
		return nil, errTokensOff
	}
	who := s.caller(h)
	if who == nil {
		return nil, errNoAccount
	}
	return who, nil
}

// Who is behind the request, nil for the daemon token or with auth off
func (s *AuthService) caller(h http.Header) *auth.Session {
	if s.guard == nil {
		return nil
	}
	sess, _ := s.guard.Identify(h)
	return sess
}

// The local account behind the request, nil for the daemon token or single sign-on
func (s *AuthService) account(h http.Header) *auth.Session {
	if sess := s.caller(h); sess != nil && sess.Provider == auth.ProviderLocal {
		return sess
	}
	return nil
}
