package db

import (
	"context"
	"database/sql"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Inserts or replaces a source with its settings
func (d *DB) PutSource(ctx context.Context, s *v1.Source) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		exec := func(q string, args ...any) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		}
		id := s.GetId()
		if err := exec(`INSERT OR REPLACE INTO sources (id, kind, name, seeded, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, enumCol(s.GetKind()), s.GetName(), boolCol(s.GetSeeded()), stamp(s.GetCreatedAt().AsTime()), stamp(s.GetUpdatedAt().AsTime())); err != nil {
			return err
		}
		if err := exec(`DELETE FROM source_config WHERE source_id = ?`, id); err != nil {
			return err
		}
		return putMap(exec, `INSERT INTO source_config (source_id, name, value) VALUES (?, ?, ?)`, id, s.GetConfig())
	})
}

// Lists every source by id
func (d *DB) ListSources(ctx context.Context) ([]*v1.Source, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT id, kind, name, seeded, created_at, updated_at FROM sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []*v1.Source
	for rows.Next() {
		s := &v1.Source{}
		var kind, created, updated string
		var seeded int
		if err := rows.Scan(&s.Id, &kind, &s.Name, &seeded, &created, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		s.Kind = v1.SourceKind(enumVal(v1.SourceKind(0).Descriptor(), kind))
		s.Seeded = seeded != 0
		s.CreatedAt = timeVal(sql.NullString{String: created, Valid: true})
		s.UpdatedAt = timeVal(sql.NullString{String: updated, Valid: true})
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, s := range out {
		if s.Config, err = d.stringMap(ctx, `SELECT name, value FROM source_config WHERE source_id = ? ORDER BY name`, s.GetId()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Removes a source with its settings, reporting existence
func (d *DB) DeleteSource(ctx context.Context, id string) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM sources WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
