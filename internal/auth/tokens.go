package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxTokenName = 64
	// Bytes of entropy in a token a user makes
	userTokenBytes = 32
	// Tokens one account may hold
	maxTokens = 100
	// Gap between last-used stamps written for one token
	touchGap = time.Minute
	// Prune the last-used table once it holds this many entries
	touchKeys = 1000
)

var (
	// Invalid token request
	ErrToken = errors.New("token")
	// No such token among the caller's
	ErrUnknownToken = errors.New("unknown token")
)

// API tokens users make in the web UI. Each acts as the account that made it
// until that account revokes it or, for local accounts, is removed.
type Tokens struct {
	db  *db.DB
	log *slog.Logger
	now func() time.Time

	mu      sync.Mutex
	touched map[string]time.Time
}

func NewTokens(store *db.DB, log *slog.Logger) *Tokens {
	return &Tokens{db: store, log: log, now: time.Now, touched: map[string]time.Time{}}
}

// The account behind a bearer token, recording the use. Lookups that fail refuse the token.
func (t *Tokens) Identify(ctx context.Context, secret string) (*Session, bool) {
	if secret == "" {
		return nil, false
	}
	row, err := t.db.FindToken(ctx, secret)
	if db.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.log.Error("token lookup failed", "err", err)
		return nil, false
	}
	t.touch(ctx, row.Token.GetId())
	return &Session{Subject: row.Subject, Name: row.Owner, Email: row.Email, Provider: row.Provider, Issued: row.Token.GetCreatedAt().AsTime()}, true
}

// Stamps the token's last use, at most once per gap
func (t *Tokens) touch(ctx context.Context, id string) {
	now := t.now()
	t.mu.Lock()
	if last, ok := t.touched[id]; ok && now.Sub(last) < touchGap {
		t.mu.Unlock()
		return
	}
	if len(t.touched) >= touchKeys {
		for k, at := range t.touched {
			if now.Sub(at) >= touchGap {
				delete(t.touched, k)
			}
		}
	}
	t.touched[id] = now
	t.mu.Unlock()
	if err := t.db.TouchToken(ctx, id, now); err != nil {
		t.log.Warn("token use not recorded", "id", id, "err", err)
	}
}

// The account's tokens, newest first
func (t *Tokens) List(ctx context.Context, who *Session) ([]*v1.ApiToken, error) {
	rows, err := t.db.ListTokens(ctx, who.Provider, who.Subject)
	if err != nil {
		return nil, err
	}
	out := make([]*v1.ApiToken, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Token)
	}
	return out, nil
}

// Makes a token for the account
func (t *Tokens) Create(ctx context.Context, who *Session, name string) (*v1.ApiToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrToken)
	}
	if len(name) > maxTokenName {
		return nil, fmt.Errorf("%w: name is longer than %d characters", ErrToken, maxTokenName)
	}
	existing, err := t.db.ListTokens(ctx, who.Provider, who.Subject)
	if err != nil {
		return nil, err
	}
	if len(existing) >= maxTokens {
		return nil, fmt.Errorf("%w: %s holds %d tokens already, revoke one first", ErrToken, who.Name, len(existing))
	}
	secret, err := RandomString(userTokenBytes)
	if err != nil {
		return nil, err
	}
	id, err := RandomString(12)
	if err != nil {
		return nil, err
	}
	tok := &v1.ApiToken{Id: id, Name: name, Secret: secret, CreatedAt: timestamppb.New(t.now().UTC())}
	if err := t.db.PutToken(ctx, db.TokenRow{Token: tok, Provider: who.Provider, Subject: who.Subject, Owner: who.Name, Email: who.Email}); err != nil {
		return nil, err
	}
	t.log.Info("api token created", "owner", who.Name, "provider", who.Provider, "name", name, "id", id)
	return tok, nil
}

// Revokes one of the account's tokens
func (t *Tokens) Delete(ctx context.Context, who *Session, id string) error {
	ok, err := t.db.DeleteToken(ctx, who.Provider, who.Subject, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownToken, id)
	}
	t.mu.Lock()
	delete(t.touched, id)
	t.mu.Unlock()
	t.log.Info("api token revoked", "owner", who.Name, "provider", who.Provider, "id", id)
	return nil
}
