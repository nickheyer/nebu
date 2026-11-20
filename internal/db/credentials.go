package db

import (
	"context"
	"database/sql"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A provider credential with its owner and its secret, which the API never answers
type CredentialRow struct {
	// Id, source, name, kind, expiry, position and times, never the secret
	Credential *v1.ProviderCredential
	Owner      string
	Secret     string
}

const credentialColumns = `id, owner, source_id, name, kind, secret, expires_at, position, created_at, updated_at`

// Inserts a credential or replaces one the same owner holds under its id, reporting whether it was written
func (d *DB) PutCredential(ctx context.Context, r CredentialRow) (bool, error) {
	c := r.Credential
	res, err := d.sql.ExecContext(ctx, `INSERT INTO provider_credentials (`+credentialColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, secret = excluded.secret, expires_at = excluded.expires_at, position = excluded.position, updated_at = excluded.updated_at
		WHERE provider_credentials.owner = excluded.owner`,
		c.GetId(), r.Owner, c.GetSourceId(), c.GetName(), enumCol(c.GetKind()), r.Secret, timeCol(c.GetExpiresAt()), c.GetPosition(), stamp(c.GetCreatedAt().AsTime()), stamp(c.GetUpdatedAt().AsTime()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Lists one owner's credentials by source and position
func (d *DB) ListCredentials(ctx context.Context, owner string) ([]CredentialRow, error) {
	return list(ctx, d, `SELECT `+credentialColumns+` FROM provider_credentials WHERE owner = ? ORDER BY source_id, position, created_at, id`, scanCredential, owner)
}

// Lists one owner's credentials for a source, the first by position first
func (d *DB) SourceCredentials(ctx context.Context, owner, sourceID string) ([]CredentialRow, error) {
	return list(ctx, d, `SELECT `+credentialColumns+` FROM provider_credentials WHERE owner = ? AND source_id = ? ORDER BY position, created_at, id`, scanCredential, owner, sourceID)
}

// One of an owner's credentials by id
func (d *DB) GetCredential(ctx context.Context, owner, id string) (CredentialRow, error) {
	rows, err := list(ctx, d, `SELECT `+credentialColumns+` FROM provider_credentials WHERE owner = ? AND id = ?`, scanCredential, owner, id)
	return one(rows, err, "credential", id)
}

// Removes one of an owner's credentials, reporting existence
func (d *DB) DeleteCredential(ctx context.Context, owner, id string) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM provider_credentials WHERE owner = ? AND id = ?`, owner, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func scanCredential(rows *sql.Rows) (CredentialRow, error) {
	r := CredentialRow{Credential: &v1.ProviderCredential{}}
	c := r.Credential
	var kind string
	if err := rows.Scan(&c.Id, &r.Owner, &c.SourceId, &c.Name, &kind, &r.Secret, at{&c.ExpiresAt}, &c.Position, at{&c.CreatedAt}, at{&c.UpdatedAt}); err != nil {
		return CredentialRow{}, err
	}
	c.Kind = enumOf[v1.CredentialKind](kind)
	c.SecretSet = r.Secret != ""
	return r, nil
}
