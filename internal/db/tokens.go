package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Provider of accounts kept in the users table, whose tokens belong to a row there
const LocalProvider = "local"

// An API token with the identity it acts as
type TokenRow struct {
	// Id, name, prefix, scopes and times, never the secret
	Token *v1.ApiToken
	// Hex sha256 of the whole token the client sends
	Hash string
	// The owner's role when the token was made, which caps its scopes
	Role v1.Role
	// local or oidc, and the account id or single sign-on subject
	Provider, Subject string
	// Username or the provider's display name, and the email when known
	Owner, Email string
}

const tokenColumns = `id, provider, subject, owner, email, name, prefix, hash, scopes, role, created_at, last_used_at`

// Inserts a token, failing on a prefix another token holds
func (d *DB) PutToken(ctx context.Context, r TokenRow) error {
	t := r.Token
	_, err := d.sql.ExecContext(ctx, `INSERT INTO api_tokens (`+tokenColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.GetId(), r.Provider, r.Subject, r.Owner, r.Email, t.GetName(), t.GetPrefix(), r.Hash, scopesCol(t.GetScopes()), enumCol(r.Role), stamp(t.GetCreatedAt().AsTime()), timeCol(t.GetLastUsedAt()))
	return err
}

// Lists one identity's tokens, newest first
func (d *DB) ListTokens(ctx context.Context, provider, subject string) ([]TokenRow, error) {
	return list(ctx, d, `SELECT `+tokenColumns+` FROM api_tokens WHERE provider = ? AND subject = ? ORDER BY created_at DESC, id`, scanToken, provider, subject)
}

// Finds the token holding a prefix, the caller comparing the hash
func (d *DB) FindToken(ctx context.Context, prefix string) (TokenRow, error) {
	rows, err := list(ctx, d, `SELECT `+tokenColumns+` FROM api_tokens WHERE prefix = ?`, scanToken, prefix)
	if err != nil {
		return TokenRow{}, err
	}
	if len(rows) == 0 {
		return TokenRow{}, ErrNotFound
	}
	return rows[0], nil
}

func scanToken(rows *sql.Rows) (TokenRow, error) {
	r := TokenRow{Token: &v1.ApiToken{}}
	t := r.Token
	var scopes string
	if err := rows.Scan(&t.Id, &r.Provider, &r.Subject, &r.Owner, &r.Email, &t.Name, &t.Prefix, &r.Hash, &scopes, enumAt[v1.Role]{&r.Role}, at{&t.CreatedAt}, at{&t.LastUsedAt}); err != nil {
		return TokenRow{}, err
	}
	t.Scopes = scopesOf(scopes)
	return r, nil
}

// Records a use of the token
func (d *DB) TouchToken(ctx context.Context, id string, used time.Time) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, stamp(used), id)
	return err
}

// Removes one of an identity's tokens, reporting existence
func (d *DB) DeleteToken(ctx context.Context, provider, subject, id string) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM api_tokens WHERE provider = ? AND subject = ? AND id = ?`, provider, subject, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Hex sha256 of a token, the form the hash column keeps
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Whether a hash column holds a hex sha256 rather than a plaintext secret from before hashing
func isTokenHash(s string) bool {
	if len(s) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

// Hashes plaintext secrets kept before hashing in place, granting them every scope the admin role they had holds
func hashLegacyTokens(ctx context.Context, d *DB) error {
	type legacy struct{ id, secret string }
	rows, err := list(ctx, d, `SELECT id, hash FROM api_tokens`, func(rows *sql.Rows) (legacy, error) {
		var l legacy
		return l, rows.Scan(&l.id, &l.secret)
	})
	if err != nil {
		return err
	}
	all := scopesCol([]v1.Scope{v1.Scope_SCOPE_INFERENCE, v1.Scope_SCOPE_READ, v1.Scope_SCOPE_OPERATE, v1.Scope_SCOPE_ADMIN})
	return d.tx(ctx, func(exec execFn) error {
		for _, l := range rows {
			if isTokenHash(l.secret) {
				continue
			}
			if err := exec(`UPDATE api_tokens SET hash = ?, scopes = ?, role = ? WHERE id = ?`, TokenHash(l.secret), all, enumCol(v1.Role_ROLE_ADMIN), l.id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Joins scopes as sorted short names
func scopesCol(scopes []v1.Scope) string {
	names := make([]string, 0, len(scopes))
	for _, s := range scopes {
		names = append(names, enumCol(s))
	}
	slices.Sort(names)
	return strings.Join(slices.Compact(names), ",")
}

// Reads joined short names back into scopes in enum order, dropping unknown names
func scopesOf(col string) []v1.Scope {
	var out []v1.Scope
	for name := range strings.SplitSeq(col, ",") {
		if s := enumOf[v1.Scope](name); s != v1.Scope_SCOPE_UNSPECIFIED {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
