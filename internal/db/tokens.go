package db

import (
	"context"
	"database/sql"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Provider of accounts kept in the users table. Tokens with it belong to a row there.
const LocalProvider = "local"

// An API token with the identity it acts as
type TokenRow struct {
	Token *v1.ApiToken
	// local or oidc, and the account id or single sign-on subject
	Provider, Subject string
	// Username or the provider's display name, and the email when known
	Owner, Email string
}

const tokenColumns = `id, provider, subject, owner, email, name, secret, created_at, last_used_at`

// Inserts a token. Duplicate secrets fail on the unique index.
func (d *DB) PutToken(ctx context.Context, r TokenRow) error {
	t := r.Token
	_, err := d.sql.ExecContext(ctx, `INSERT INTO api_tokens (`+tokenColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.GetId(), r.Provider, r.Subject, r.Owner, r.Email, t.GetName(), t.GetSecret(), stamp(t.GetCreatedAt().AsTime()), timeCol(t.GetLastUsedAt()))
	return err
}

// Lists one identity's tokens, newest first
func (d *DB) ListTokens(ctx context.Context, provider, subject string) ([]TokenRow, error) {
	return list(ctx, d, `SELECT `+tokenColumns+` FROM api_tokens WHERE provider = ? AND subject = ? ORDER BY created_at DESC, id`, scanToken, provider, subject)
}

// Finds the token a client sent
func (d *DB) FindToken(ctx context.Context, secret string) (TokenRow, error) {
	rows, err := list(ctx, d, `SELECT `+tokenColumns+` FROM api_tokens WHERE secret = ?`, scanToken, secret)
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
	if err := rows.Scan(&t.Id, &r.Provider, &r.Subject, &r.Owner, &r.Email, &t.Name, &t.Secret, at{&t.CreatedAt}, at{&t.LastUsedAt}); err != nil {
		return TokenRow{}, err
	}
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
