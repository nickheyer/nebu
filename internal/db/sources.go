package db

import (
	"context"
	"database/sql"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Inserts or replaces a source with its settings
func (d *DB) PutSource(ctx context.Context, s *v1.Source) error {
	return d.tx(ctx, func(exec execFn) error {
		id := s.GetId()
		if err := exec(`INSERT OR REPLACE INTO sources (id, kind, name, seeded, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, enumCol(s.GetKind()), s.GetName(), boolCol(s.GetSeeded()), stamp(s.GetCreatedAt().AsTime()), stamp(s.GetUpdatedAt().AsTime())); err != nil {
			return err
		}
		if err := clearChildren(exec, "source_id", id, "source_config"); err != nil {
			return err
		}
		return putMap(exec, `INSERT INTO source_config (source_id, name, value) VALUES (?, ?, ?)`, id, s.GetConfig())
	})
}

// Lists every source by id
func (d *DB) ListSources(ctx context.Context) ([]*v1.Source, error) {
	out, err := list(ctx, d, `SELECT id, kind, name, seeded, created_at, updated_at FROM sources ORDER BY id`, func(rows *sql.Rows) (*v1.Source, error) {
		s := &v1.Source{}
		return s, rows.Scan(&s.Id, enumAt[v1.SourceKind]{&s.Kind}, &s.Name, (*flag)(&s.Seeded), at{&s.CreatedAt}, at{&s.UpdatedAt})
	})
	if err != nil {
		return nil, err
	}
	for _, s := range out {
		if s.Config, err = d.stringMap(ctx, `SELECT name, value FROM source_config WHERE source_id = ? ORDER BY name`, s.GetId()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Removes a source with its settings, reporting existence
func (d *DB) DeleteSource(ctx context.Context, id string) (bool, error) {
	return d.del(ctx, "sources", "id", id)
}

// One learned correction with its key
type CalibrationRow struct {
	RuntimeID    string
	Architecture string
	Calibration  *v1.Calibration
}

// Inserts or replaces one calibration
func (d *DB) PutCalibration(ctx context.Context, runtimeID, architecture string, c *v1.Calibration) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO calibrations (runtime_id, architecture, overhead_delta, samples, updated_at) VALUES (?, ?, ?, ?, ?)`,
		runtimeID, architecture, c.GetOverheadDelta(), c.GetSamples(), stamp(c.GetUpdatedAt().AsTime()))
	return err
}

// Lists every calibration
func (d *DB) ListCalibrations(ctx context.Context) ([]CalibrationRow, error) {
	return list(ctx, d, `SELECT runtime_id, architecture, overhead_delta, samples, updated_at FROM calibrations ORDER BY runtime_id, architecture`, func(rows *sql.Rows) (CalibrationRow, error) {
		r := CalibrationRow{Calibration: &v1.Calibration{}}
		return r, rows.Scan(&r.RuntimeID, &r.Architecture, &r.Calibration.OverheadDelta, &r.Calibration.Samples, at{&r.Calibration.UpdatedAt})
	})
}
