package db

import (
	"context"
	"database/sql"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// A provider credential with its owner and its secret, which the API never answers
type CredentialRow struct {
	// Id, source, name, kind, expiry, position and times, never the secret
	Credential *v1.ProviderCredential
	Owner      string
	Secret     string
}

const credentialColumns = `id, owner, source_id, name, kind, secret, expires_at, position, created_at, updated_at, budgets`

// Stores budgets as JSON inside a credential wrapper, empty text when there are none
func budgetsCol(list []*v1.CredentialBudget) (string, error) {
	if len(list) == 0 {
		return "", nil
	}
	data, err := protojson.Marshal(&v1.ProviderCredential{Budgets: list})
	return string(data), err
}

// Inserts a credential or replaces one the same owner holds under its id, reporting whether it was written
func (d *DB) PutCredential(ctx context.Context, r CredentialRow) (bool, error) {
	c := r.Credential
	budgets, err := budgetsCol(c.GetBudgets())
	if err != nil {
		return false, err
	}
	res, err := d.sql.ExecContext(ctx, `INSERT INTO provider_credentials (`+credentialColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, secret = excluded.secret, expires_at = excluded.expires_at, position = excluded.position, updated_at = excluded.updated_at, budgets = excluded.budgets
		WHERE provider_credentials.owner = excluded.owner`,
		c.GetId(), r.Owner, c.GetSourceId(), c.GetName(), enumCol(c.GetKind()), r.Secret, timeCol(c.GetExpiresAt()), c.GetPosition(), stamp(c.GetCreatedAt().AsTime()), stamp(c.GetUpdatedAt().AsTime()), budgets)
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
	var kind, budgets string
	if err := rows.Scan(&c.Id, &r.Owner, &c.SourceId, &c.Name, &kind, &r.Secret, at{&c.ExpiresAt}, &c.Position, at{&c.CreatedAt}, at{&c.UpdatedAt}, &budgets); err != nil {
		return CredentialRow{}, err
	}
	c.Kind = enumOf[v1.CredentialKind](kind)
	c.SecretSet = r.Secret != ""
	if budgets != "" {
		wrapped := &v1.ProviderCredential{}
		if err := unmarshalJSON(budgets, wrapped); err != nil {
			return CredentialRow{}, err
		}
		c.Budgets = wrapped.GetBudgets()
	}
	return r, nil
}

// Seconds one usage bucket spans
const UsageBucket = 300

// Requests and tokens one credential was used for with one model, in one bucket or summed from the first bucket on
type UsageRow struct {
	Usage  *v1.CredentialUsage
	Bucket int64
}

// Adds each row's counts to what its credential, model and bucket hold in one transaction, rows of a removed credential dropped
func (d *DB) AddUsage(ctx context.Context, rows []UsageRow) error {
	return d.tx(ctx, func(exec execFn) error {
		for _, r := range rows {
			u := r.Usage
			if err := exec(`INSERT INTO credential_usage (credential_id, model, bucket, requests, input_tokens, output_tokens, cached_tokens)
				SELECT ?, ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM provider_credentials WHERE id = ?)
				ON CONFLICT (credential_id, model, bucket) DO UPDATE SET requests = requests + excluded.requests, input_tokens = input_tokens + excluded.input_tokens,
				output_tokens = output_tokens + excluded.output_tokens, cached_tokens = cached_tokens + excluded.cached_tokens`,
				u.GetCredentialId(), u.GetModel(), r.Bucket, u.GetRequests(), u.GetInputTokens(), u.GetOutputTokens(), u.GetCachedTokens(), u.GetCredentialId()); err != nil {
				return err
			}
		}
		return nil
	})
}

// An owner's usage from bucket from up to bucket to by credential and model, one credential's when credentialID is set,
// each row's bucket the first holding any, the most requests first
func (d *DB) CredentialUsage(ctx context.Context, owner, credentialID string, from, to int64) ([]UsageRow, error) {
	query := `SELECT u.credential_id, u.model, SUM(u.requests), SUM(u.input_tokens), SUM(u.output_tokens), SUM(u.cached_tokens), MIN(u.bucket)
		FROM credential_usage u JOIN provider_credentials c ON c.id = u.credential_id
		WHERE c.owner = ? AND u.bucket >= ? AND u.bucket < ?`
	args := []any{owner, from, to}
	if credentialID != "" {
		query, args = query+` AND u.credential_id = ?`, append(args, credentialID)
	}
	return list(ctx, d, query+` GROUP BY u.credential_id, u.model ORDER BY SUM(u.requests) DESC, u.credential_id, u.model`, func(rows *sql.Rows) (UsageRow, error) {
		r := UsageRow{Usage: &v1.CredentialUsage{}}
		u := r.Usage
		err := rows.Scan(&u.CredentialId, &u.Model, &u.Requests, &u.InputTokens, &u.OutputTokens, &u.CachedTokens, &r.Bucket)
		return r, err
	}, args...)
}

// Removes usage in buckets before the one given, answering the rows removed
func (d *DB) PruneUsage(ctx context.Context, before int64) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM credential_usage WHERE bucket < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
