package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A provider credential with its owner and its secret, which the API never answers
type CredentialRow struct {
	// Id, source, name, kind, expiry, position and times, never the secret
	Credential *v1.ProviderCredential
	Owner      string
	Secret     string
}

const credentialColumns = `id, owner, source_id, name, kind, secret, expires_at, position, created_at, updated_at, budgets`

// Stores budgets as JSON inside a credential wrapper, empty text when there are none, each enum by its number so a
// release naming a value otherwise, before or after this one, reads the same budget
func budgetsCol(list []*v1.CredentialBudget) (string, error) {
	if len(list) == 0 {
		return "", nil
	}
	data, err := protojson.MarshalOptions{UseEnumNumbers: true}.Marshal(&v1.ProviderCredential{Budgets: list})
	return string(data), err
}

// Inserts a new credential, one already kept changed column by column below so that a key replaced, a sign-in
// refreshed and a reorder never write over one another
func (d *DB) PutCredential(ctx context.Context, r CredentialRow) error {
	c := r.Credential
	budgets, err := budgetsCol(c.GetBudgets())
	if err != nil {
		return err
	}
	_, err = d.sql.ExecContext(ctx, `INSERT INTO provider_credentials (`+credentialColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.GetId(), r.Owner, c.GetSourceId(), c.GetName(), enumCol(c.GetKind()), r.Secret, timeCol(c.GetExpiresAt()), c.GetPosition(), stamp(c.GetCreatedAt().AsTime()), stamp(c.GetUpdatedAt().AsTime()), budgets)
	return err
}

// Sets what an owner edits of one of their credentials, an empty name or key leaving the stored one and the budgets
// replaced only when set, reporting whether the owner holds it
func (d *DB) EditCredential(ctx context.Context, owner, id, name, key string, budgets []*v1.CredentialBudget, set bool, changed time.Time) (bool, error) {
	col, err := budgetsCol(budgets)
	if err != nil {
		return false, err
	}
	return d.credentialSet(ctx, `name = COALESCE(NULLIF(?, ''), name), secret = COALESCE(NULLIF(?, ''), secret), budgets = CASE WHEN ? THEN ? ELSE budgets END, updated_at = ?`,
		owner, id, name, key, set, col, stamp(changed))
}

// Stores the secret of one of an owner's credentials with when it lapses, a refreshed sign-in's tokens, reporting whether the owner holds it
func (d *DB) SetCredentialSecret(ctx context.Context, owner, id, secret string, expires *timestamppb.Timestamp, changed time.Time) (bool, error) {
	return d.credentialSet(ctx, `secret = ?, expires_at = ?, updated_at = ?`, owner, id, secret, timeCol(expires), stamp(changed))
}

// Moves one of an owner's credentials to a position among those of its source, reporting whether the owner holds it
func (d *DB) SetCredentialPosition(ctx context.Context, owner, id string, position uint32) (bool, error) {
	return d.credentialSet(ctx, `position = ?`, owner, id, position)
}

// Sets the named columns of one of an owner's credentials and nothing else, reporting whether the owner holds it
func (d *DB) credentialSet(ctx context.Context, set, owner, id string, values ...any) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `UPDATE provider_credentials SET `+set+` WHERE owner = ? AND id = ?`, append(values, owner, id)...)
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

// The id of every credential kept, whoever owns it
func (d *DB) CredentialIDs(ctx context.Context) (map[string]struct{}, error) {
	ids, err := list(ctx, d, `SELECT id FROM provider_credentials`, func(rows *sql.Rows) (string, error) {
		var id string
		return id, rows.Scan(&id)
	})
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out, err
}

// One of an owner's credentials by id
func (d *DB) GetCredential(ctx context.Context, owner, id string) (CredentialRow, error) {
	rows, err := list(ctx, d, `SELECT `+credentialColumns+` FROM provider_credentials WHERE owner = ? AND id = ?`, scanCredential, owner, id)
	return one(rows, err, "credential", id)
}

// Removes one of an owner's credentials, the rest for its source renumbered from 1 in their order, reporting existence.
// A revocation given is queued in the same write, so a removed sign-in's token is never left without one.
func (d *DB) DeleteCredential(ctx context.Context, owner, id string, revoke *Revocation) (bool, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var sourceID string
	err = tx.QueryRowContext(ctx, `DELETE FROM provider_credentials WHERE owner = ? AND id = ? RETURNING source_id`, owner, id).Scan(&sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE provider_credentials SET position = ranked.n
		FROM (SELECT id, ROW_NUMBER() OVER (ORDER BY position, created_at, id) AS n FROM provider_credentials WHERE owner = ? AND source_id = ?) AS ranked
		WHERE provider_credentials.id = ranked.id AND provider_credentials.position != ranked.n`, owner, sourceID); err != nil {
		return false, err
	}
	if revoke != nil {
		if _, err := tx.ExecContext(ctx, queueRevocation, revoke.ID, revoke.Endpoint, revoke.ClientID, revoke.Token, stamp(revoke.Since)); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// A refresh token its provider has yet to confirm revoking: where it is revoked, the client it was issued to, since when
// it waits, and when and how often the provider was asked
type Revocation struct {
	ID, Endpoint, ClientID, Token string
	Since, Tried                  time.Time
	Tries                         int
}

const queueRevocation = `INSERT INTO credential_revocations (id, endpoint, client_id, token, created_at) VALUES (?, ?, ?, ?, ?)
	ON CONFLICT (id) DO UPDATE SET endpoint = excluded.endpoint, client_id = excluded.client_id, token = excluded.token, created_at = excluded.created_at, tried_at = NULL, tries = 0`

// Queues a refresh token for revocation
func (d *DB) QueueRevocation(ctx context.Context, r Revocation) error {
	_, err := d.sql.ExecContext(ctx, queueRevocation, r.ID, r.Endpoint, r.ClientID, r.Token, stamp(r.Since))
	return err
}

// Every refresh token waiting to be revoked, the longest waiting first
func (d *DB) Revocations(ctx context.Context) ([]Revocation, error) {
	return list(ctx, d, `SELECT id, endpoint, client_id, token, created_at, tried_at, tries FROM credential_revocations ORDER BY created_at, id`, func(rows *sql.Rows) (Revocation, error) {
		var r Revocation
		var since string
		var tried sql.NullString
		if err := rows.Scan(&r.ID, &r.Endpoint, &r.ClientID, &r.Token, &since, &tried, &r.Tries); err != nil {
			return Revocation{}, err
		}
		r.Since, _ = time.Parse(time.RFC3339Nano, since)
		r.Tried, _ = time.Parse(time.RFC3339Nano, tried.String)
		return r, nil
	})
}

// Notes that a provider was asked to revoke a token and did not
func (d *DB) TriedRevocation(ctx context.Context, id string, at time.Time) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE credential_revocations SET tried_at = ?, tries = tries + 1 WHERE id = ?`, stamp(at), id)
	return err
}

// Forgets a token once it is revoked or its provider will never take the revocation
func (d *DB) DropRevocation(ctx context.Context, id string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM credential_revocations WHERE id = ?`, id)
	return err
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
