package db

import (
	"context"
	"database/sql"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

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
	rows, err := d.sql.QueryContext(ctx, `SELECT runtime_id, architecture, overhead_delta, samples, updated_at FROM calibrations ORDER BY runtime_id, architecture`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalibrationRow
	for rows.Next() {
		var r CalibrationRow
		var updated string
		c := &v1.Calibration{}
		if err := rows.Scan(&r.RuntimeID, &r.Architecture, &c.OverheadDelta, &c.Samples, &updated); err != nil {
			return nil, err
		}
		c.UpdatedAt = timeVal(sql.NullString{String: updated, Valid: true})
		r.Calibration = c
		out = append(out, r)
	}
	return out, rows.Err()
}
