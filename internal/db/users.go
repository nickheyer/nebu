package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Returned when the first account is created after another account exists
var ErrAccountsExist = errors.New("an account already exists")

// A local account with its password hash
type UserRow struct {
	User *v1.User
	Hash string
}

const userColumns = `id, username, password_hash, role, created_at, updated_at`

// Inserts an account, a duplicate username failing on the unique index
func (d *DB) PutUser(ctx context.Context, u *v1.User, hash string) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO users (`+userColumns+`) VALUES (?, ?, ?, ?, ?, ?)`,
		u.GetId(), u.GetUsername(), hash, enumCol(u.GetRole()), stamp(u.GetCreatedAt().AsTime()), stamp(u.GetUpdatedAt().AsTime()))
	return err
}

// Inserts the first account in one transaction that fails with ErrAccountsExist when any account exists
func (d *DB) CreateFirstUser(ctx context.Context, u *v1.User, hash string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrAccountsExist
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (`+userColumns+`) VALUES (?, ?, ?, ?, ?, ?)`,
		u.GetId(), u.GetUsername(), hash, enumCol(u.GetRole()), stamp(u.GetCreatedAt().AsTime()), stamp(u.GetUpdatedAt().AsTime())); err != nil {
		return err
	}
	return tx.Commit()
}

// Lists accounts by username
func (d *DB) ListUsers(ctx context.Context) ([]UserRow, error) {
	return list(ctx, d, `SELECT `+userColumns+` FROM users ORDER BY username`, scanUser)
}

// Finds one account by username
func (d *DB) GetUser(ctx context.Context, username string) (UserRow, error) {
	rows, err := list(ctx, d, `SELECT `+userColumns+` FROM users WHERE username = ?`, scanUser, username)
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
	if err := rows.Scan(&u.Id, &u.Username, &hash, enumAt[v1.Role]{&u.Role}, at{&u.CreatedAt}, at{&u.UpdatedAt}); err != nil {
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

// Gives an account and the API tokens it made a role, returning the account id, empty when there was none
func (d *DB) SetUserRole(ctx context.Context, provider, username string, role v1.Role) (string, error) {
	row, err := d.GetUser(ctx, username)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	id := row.User.GetId()
	return id, d.tx(ctx, func(exec execFn) error {
		if err := exec(`UPDATE users SET role = ? WHERE id = ?`, enumCol(role), id); err != nil {
			return err
		}
		return exec(`UPDATE api_tokens SET role = ? WHERE provider = ? AND subject = ?`, enumCol(role), provider, id)
	})
}

// Removes an account and the API tokens it made, returning the account id, empty when there was none
func (d *DB) DeleteUser(ctx context.Context, username string) (string, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE provider = ? AND subject = ?`, LocalProvider, id); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

// Makes every account from before roles an admin, the full access each held
func adminLegacyUsers(ctx context.Context, d *DB) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE users SET role = ? WHERE role = ''`, enumCol(v1.Role_ROLE_ADMIN))
	return err
}
