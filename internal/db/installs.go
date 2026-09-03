package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Inserts or replaces an install and its facts
func (d *DB) PutInstall(ctx context.Context, in *v1.Install) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO installs (id, runtime_id, kind, path, dir, version, origin, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			in.GetId(), in.GetRuntimeId(), enumCol(in.GetKind()), in.GetPath(), in.GetDir(), in.GetVersion(), in.GetOrigin(), stamp(in.GetCreatedAt().AsTime())); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM install_facts WHERE install_id = ?`, in.GetId()); err != nil {
			return err
		}
		return putMap(func(q string, args ...any) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		}, `INSERT INTO install_facts (install_id, key, value) VALUES (?, ?, ?)`, in.GetId(), in.GetFacts())
	})
}

// Returns one install by id
func (d *DB) GetInstall(ctx context.Context, id string) (*v1.Install, error) {
	list, err := d.installs(ctx, `WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%w: install %q", ErrNotFound, id)
	}
	return list[0], nil
}

// Lists installs newest first, all runtimes when runtimeID is empty
func (d *DB) ListInstalls(ctx context.Context, runtimeID string) ([]*v1.Install, error) {
	if runtimeID == "" {
		return d.installs(ctx, ``)
	}
	return d.installs(ctx, `WHERE runtime_id = ?`, runtimeID)
}

// Removes an install, reporting whether it existed
func (d *DB) DeleteInstall(ctx context.Context, id string) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM installs WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (d *DB) installs(ctx context.Context, where string, args ...any) ([]*v1.Install, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT id, runtime_id, kind, path, dir, version, origin, created_at FROM installs `+where+` ORDER BY created_at DESC, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*v1.Install
	for rows.Next() {
		in := &v1.Install{}
		var kind, created string
		if err := rows.Scan(&in.Id, &in.RuntimeId, &kind, &in.Path, &in.Dir, &in.Version, &in.Origin, &created); err != nil {
			return nil, err
		}
		in.Kind = v1.InstallKind(enumVal(v1.InstallKind(0).Descriptor(), kind))
		in.CreatedAt = timeVal(sql.NullString{String: created, Valid: true})
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, in := range out {
		facts, err := d.stringMap(ctx, `SELECT key, value FROM install_facts WHERE install_id = ? ORDER BY key`, in.GetId())
		if err != nil {
			return nil, err
		}
		in.Facts = facts
	}
	return out, nil
}

func (d *DB) stringMap(ctx context.Context, query string, args ...any) (map[string]string, error) {
	rows, err := d.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// Reports whether err means a missing record
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
