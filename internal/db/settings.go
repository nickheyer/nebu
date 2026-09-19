package db

import (
	"context"
	"database/sql"
	"encoding/json"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// Reads one settings field per row. Missing rows keep zero values.
func (d *DB) GetSettings(ctx context.Context) (*v1.Settings, error) {
	fields := map[string]json.RawMessage{}
	_, err := list(ctx, d, `SELECT key, value FROM settings`, func(rows *sql.Rows) (struct{}, error) {
		var k, v string
		err := rows.Scan(&k, &v)
		fields[k] = json.RawMessage(v)
		return struct{}{}, err
	})
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	out := &v1.Settings{}
	return out, protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(data, out)
}

// Replaces settings rows keyed by proto field name.
func (d *DB) PutSettings(ctx context.Context, s *v1.Settings) error {
	data, err := protojson.MarshalOptions{UseProtoNames: true, EmitDefaultValues: true}.Marshal(s)
	if err != nil {
		return err
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	return d.tx(ctx, func(exec execFn) error {
		if err := exec(`DELETE FROM settings`); err != nil {
			return err
		}
		for k, v := range fields {
			if err := exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, k, string(v)); err != nil {
				return err
			}
		}
		return nil
	})
}
