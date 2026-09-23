package db

import (
	"context"
	"database/sql"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A local account with its password hash
type UserRow struct {
	User *v1.User
	Hash string
}

// Inserts an account. Duplicate usernames fail on the unique index.
func (d *DB) PutUser(ctx context.Context, u *v1.User, hash string) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO users (id, username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		u.GetId(), u.GetUsername(), hash, stamp(u.GetCreatedAt().AsTime()), stamp(u.GetUpdatedAt().AsTime()))
	return err
}

// Lists accounts by username
func (d *DB) ListUsers(ctx context.Context) ([]UserRow, error) {
	return list(ctx, d, `SELECT id, username, password_hash, created_at, updated_at FROM users ORDER BY username`, scanUser)
}

// Finds one account by username
func (d *DB) GetUser(ctx context.Context, username string) (UserRow, error) {
	rows, err := list(ctx, d, `SELECT id, username, password_hash, created_at, updated_at FROM users WHERE username = ?`, scanUser, username)
	if err != nil {
		return UserRow{}, err
	}
	if len(rows) == 0 {
		return UserRow{}, ErrNotFound
	}
	return rows[0], nil
}

func scanUser(rows *sql.Rows) (UserRow, error) {
	u := &v1.User{}
	var hash string
	if err := rows.Scan(&u.Id, &u.Username, &hash, at{&u.CreatedAt}, at{&u.UpdatedAt}); err != nil {
		return UserRow{}, err
	}
	return UserRow{User: u, Hash: hash}, nil
}

// Replaces an account's password hash and marks the change time, reporting existence
func (d *DB) SetUserPassword(ctx context.Context, username, hash string, changed time.Time) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE username = ?`, hash, stamp(changed), username)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Removes an account and the API tokens it made, reporting existence
func (d *DB) DeleteUser(ctx context.Context, username string) (bool, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE provider = ? AND subject IN (SELECT id FROM users WHERE username = ?)`, LocalProvider, username); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE username = ?`, username)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, tx.Commit()
}
