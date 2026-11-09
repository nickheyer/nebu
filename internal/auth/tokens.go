package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxTokenName = 64
	// What every token a user makes starts with
	tokenMark = "nebu_"
	// Characters of the prefix a token is looked up by
	prefixLen = 12
	// Random bytes behind a token's secret
	secretBytes = 32
	// Base62 characters 32 bytes take, since 62 to the 43rd exceeds 2 to the 256th
	secretLen = 43
	// Tokens one account may hold
	maxTokens = 100
	// Gap between last-used stamps written for one token
	touchGap = time.Minute
	// Prune the last-used table once it holds this many entries
	touchKeys = 1000
	// How long a token found in the store answers requests without it
	cacheTTL = 30 * time.Second
	// Prune the lookup cache of expired entries once it holds this many
	cacheKeys = 1000
	base62    = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

var (
	// Invalid token request
	ErrToken = errors.New("token")
	// No such token among the caller's
	ErrUnknownToken = errors.New("unknown token")
	// The daemon token and auth.disabled name no account to hold tokens
	ErrNoAccount = errors.New("sign in with an account to manage its API tokens, the daemon token has none")
	// The caller's role or scopes do not open the change
	ErrForbidden = errors.New("permission denied")
)

// API tokens users make, each acting as its account within its scopes until revoked or the account is removed
type Tokens struct {
	db  *db.DB
	log *slog.Logger
	now func() time.Time

	mu      sync.Mutex
	touched map[string]time.Time
	// Rows found in the store by the hash of the whole token, each answering until it expires
	cache map[[sha256.Size]byte]cachedToken
}

// A token row and when it stops answering without the store
type cachedToken struct {
	row     db.TokenRow
	expires time.Time
}

func NewTokens(store *db.DB, log *slog.Logger) *Tokens {
	return &Tokens{db: store, log: log, now: time.Now, touched: map[string]time.Time{}, cache: map[[sha256.Size]byte]cachedToken{}}
}

// The prefix a token is stored under, the twelve characters after nebu_ or an older token's first twelve
func tokenPrefix(token string) (string, bool) {
	if len(token) == len(tokenMark)+prefixLen+secretLen && strings.HasPrefix(token, tokenMark) {
		return token[len(tokenMark) : len(tokenMark)+prefixLen], true
	}
	if len(token) < prefixLen {
		return "", false
	}
	return token[:prefixLen], true
}

// The identity behind a bearer token, found by prefix and matched by hash in constant time, recording the use
func (t *Tokens) Identify(ctx context.Context, token string) (Identity, bool) {
	prefix, ok := tokenPrefix(token)
	if !ok {
		return Identity{}, false
	}
	key := sha256.Sum256([]byte(token))
	now := t.now()
	t.mu.Lock()
	c, cached := t.cache[key]
	t.mu.Unlock()
	if !cached || !now.Before(c.expires) {
		row, err := t.db.FindToken(ctx, prefix)
		if db.IsNotFound(err) {
			if cached {
				t.forget(func(r db.TokenRow) bool { return r.Token.GetId() == c.row.Token.GetId() })
			}
			return Identity{}, false
		}
		if err != nil {
			t.log.Error("token lookup failed", "err", err)
			return Identity{}, false
		}
		if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(key[:])), []byte(row.Hash)) != 1 {
			return Identity{}, false
		}
		c = cachedToken{row: row, expires: now.Add(cacheTTL)}
		t.mu.Lock()
		if len(t.cache) >= cacheKeys {
			for k, old := range t.cache {
				if !now.Before(old.expires) {
					delete(t.cache, k)
				}
			}
		}
		t.cache[key] = c
		t.mu.Unlock()
	}
	t.touch(ctx, c.row.Token.GetId())
	return identityOf(c.row), true
}

// The identity a token row acts as, its scopes capped by the role it was made under
func identityOf(r db.TokenRow) Identity {
	return Identity{Subject: r.Subject, Provider: r.Provider, Name: r.Owner, Email: r.Email, Role: r.Role, Scopes: capScopes(r.Token.GetScopes(), r.Role), TokenPrefix: r.Token.GetPrefix()}
}

// Drops cached rows the predicate names, so a revoked token stops answering at once
func (t *Tokens) forget(gone func(db.TokenRow) bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, c := range t.cache {
		if gone(c.row) {
			delete(t.cache, k)
		}
	}
}

// Drops every cached token of an account after the account is removed, a nil table holding none
func (t *Tokens) ForgetAccount(provider, subject string) {
	if t == nil {
		return
	}
	t.forget(func(r db.TokenRow) bool { return r.Provider == provider && r.Subject == subject })
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

// The account's tokens newest first, by prefix and never with a secret
func (t *Tokens) List(ctx context.Context, who Identity) ([]*v1.ApiToken, error) {
	if who.Subject == "" {
		return nil, ErrNoAccount
	}
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

// Makes a token for the account holding scopes the caller's own credential opens, returning the whole token once
func (t *Tokens) Create(ctx context.Context, who Identity, name string, scopes []v1.Scope) (*v1.ApiToken, error) {
	if who.Subject == "" {
		return nil, ErrNoAccount
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrToken)
	}
	if len(name) > maxTokenName {
		return nil, fmt.Errorf("%w: name is longer than %d characters", ErrToken, maxTokenName)
	}
	if len(scopes) == 0 {
		return nil, fmt.Errorf("%w: choose at least one scope of inference, read, operate, or admin", ErrToken)
	}
	for _, s := range scopes {
		if _, known := v1.Scope_name[int32(s)]; !known || s == v1.Scope_SCOPE_UNSPECIFIED {
			return nil, fmt.Errorf("%w: scope %d is not inference, read, operate, or admin", ErrToken, s)
		}
		if !who.Allows(s) {
			return nil, fmt.Errorf("%w: the %s role cannot grant the %s scope", ErrForbidden, RoleName(who.Role), ScopeName(s))
		}
	}
	existing, err := t.db.ListTokens(ctx, who.Provider, who.Subject)
	if err != nil {
		return nil, err
	}
	if len(existing) >= maxTokens {
		return nil, fmt.Errorf("%w: %s holds %d tokens already, revoke one first", ErrToken, who.Name, len(existing))
	}
	prefix, err := randomBase62(prefixLen)
	if err != nil {
		return nil, err
	}
	secret, err := secretBase62()
	if err != nil {
		return nil, err
	}
	id, err := RandomString(12)
	if err != nil {
		return nil, err
	}
	whole := tokenMark + prefix + secret
	granted := slices.Sorted(slices.Values(scopes))
	tok := &v1.ApiToken{Id: id, Name: name, Prefix: prefix, Scopes: slices.Compact(granted), CreatedAt: timestamppb.New(t.now().UTC())}
	row := db.TokenRow{Token: tok, Hash: db.TokenHash(whole), Role: who.Role, Provider: who.Provider, Subject: who.Subject, Owner: who.Name, Email: who.Email}
	if err := t.db.PutToken(ctx, row); err != nil {
		return nil, err
	}
	t.log.Info("api token created", "owner", who.Name, "provider", who.Provider, "name", name, "prefix", prefix)
	out := proto.Clone(tok).(*v1.ApiToken)
	out.Secret = whole
	return out, nil
}

// Revokes one of the account's tokens
func (t *Tokens) Delete(ctx context.Context, who Identity, id string) error {
	if who.Subject == "" {
		return ErrNoAccount
	}
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
	t.forget(func(r db.TokenRow) bool { return r.Token.GetId() == id })
	t.log.Info("api token revoked", "owner", who.Name, "provider", who.Provider, "id", id)
	return nil
}

// Uniform random base62 characters, rejecting bytes past the largest multiple of 62
func randomBase62(n int) (string, error) {
	out := make([]byte, 0, n)
	buf := make([]byte, 2*n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b < 248 && len(out) < n {
				out = append(out, base62[b%62])
			}
		}
	}
	return string(out), nil
}

// 32 random bytes as base62, left padded to a fixed 43 characters
func secretBase62() (string, error) {
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	n := new(big.Int).SetBytes(raw)
	base, rem := big.NewInt(62), new(big.Int)
	out := make([]byte, secretLen)
	for i := secretLen - 1; i >= 0; i-- {
		n.DivMod(n, base, rem)
		out[i] = base62[rem.Int64()]
	}
	return string(out), nil
}
