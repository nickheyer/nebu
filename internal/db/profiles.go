package db

import (
	"context"
	"database/sql"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Inserts or replaces a profile with its params
func (d *DB) PutProfile(ctx context.Context, p *v1.Profile) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		exec := func(q string, args ...any) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		}
		id := p.GetId()
		if err := exec(`INSERT OR REPLACE INTO profiles (id, runtime_id, name, description, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, p.GetRuntimeId(), p.GetName(), p.GetDescription(), boolCol(p.GetDefault()), stamp(p.GetCreatedAt().AsTime()), stamp(p.GetUpdatedAt().AsTime())); err != nil {
			return err
		}
		if err := exec(`DELETE FROM profile_params WHERE profile_id = ?`, id); err != nil {
			return err
		}
		return putMap(exec, `INSERT INTO profile_params (profile_id, name, value) VALUES (?, ?, ?)`, id, p.GetParams())
	})
}

// Lists every profile by runtime then name
func (d *DB) ListProfiles(ctx context.Context) ([]*v1.Profile, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT id, runtime_id, name, description, is_default, created_at, updated_at FROM profiles ORDER BY runtime_id, name`)
	if err != nil {
		return nil, err
	}
	var out []*v1.Profile
	for rows.Next() {
		p := &v1.Profile{}
		var def int
		var created, updated string
		if err := rows.Scan(&p.Id, &p.RuntimeId, &p.Name, &p.Description, &def, &created, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		p.Default = def != 0
		p.CreatedAt = timeVal(sql.NullString{String: created, Valid: true})
		p.UpdatedAt = timeVal(sql.NullString{String: updated, Valid: true})
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, p := range out {
		if p.Params, err = d.stringMap(ctx, `SELECT name, value FROM profile_params WHERE profile_id = ? ORDER BY name`, p.GetId()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Removes a profile with its params, reporting existence
func (d *DB) DeleteProfile(ctx context.Context, id string) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM profiles WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
