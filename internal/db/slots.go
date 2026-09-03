package db

import (
	"context"
	"database/sql"
	"fmt"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Inserts or replaces a slot with devices, params, and request
func (d *DB) PutSlot(ctx context.Context, s *v1.Slot) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		exec := func(q string, args ...any) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		}
		id := s.GetId()
		if err := exec(`INSERT OR REPLACE INTO slots (id, name, description, runtime_id, memory_bytes, instance_id, state, error, task_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, s.GetName(), s.GetDescription(), s.GetRuntimeId(), int64(s.GetMemoryBytes()), s.GetInstanceId(), enumCol(s.GetState()), s.GetError(), s.GetTaskId(), stamp(s.GetCreatedAt().AsTime()), stamp(s.GetUpdatedAt().AsTime())); err != nil {
			return err
		}
		for _, table := range []string{"slot_devices", "slot_params", "slot_requests", "slot_request_params"} {
			if err := exec(`DELETE FROM `+table+` WHERE slot_id = ?`, id); err != nil {
				return err
			}
		}
		for i, dev := range s.GetDeviceIds() {
			if err := exec(`INSERT INTO slot_devices (slot_id, position, device_id) VALUES (?, ?, ?)`, id, i, dev); err != nil {
				return err
			}
		}
		if err := putMap(exec, `INSERT INTO slot_params (slot_id, name, value) VALUES (?, ?, ?)`, id, s.GetParams()); err != nil {
			return err
		}
		if req := s.GetRequest(); req != nil {
			if err := exec(`INSERT INTO slot_requests (slot_id, source_id, repo, weight_group, runtime_id, install_id, name) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				id, req.GetSourceId(), req.GetRepo(), req.GetGroup(), req.GetRuntimeId(), req.GetInstallId(), req.GetName()); err != nil {
				return err
			}
			if err := putMap(exec, `INSERT INTO slot_request_params (slot_id, name, value) VALUES (?, ?, ?)`, id, req.GetParams()); err != nil {
				return err
			}
		}
		return nil
	})
}

// Lists every slot by name
func (d *DB) ListSlots(ctx context.Context) ([]*v1.Slot, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT id, name, description, runtime_id, memory_bytes, instance_id, state, error, task_id, created_at, updated_at FROM slots ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var out []*v1.Slot
	for rows.Next() {
		s := &v1.Slot{}
		var memory int64
		var state, created, updated string
		if err := rows.Scan(&s.Id, &s.Name, &s.Description, &s.RuntimeId, &memory, &s.InstanceId, &state, &s.Error, &s.TaskId, &created, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		s.MemoryBytes = uint64(memory)
		s.State = v1.SlotState(enumVal(v1.SlotState(0).Descriptor(), state))
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
		if s.DeviceIds, err = d.strings(ctx, `SELECT device_id FROM slot_devices WHERE slot_id = ? ORDER BY position`, s.GetId()); err != nil {
			return nil, err
		}
		if s.Params, err = d.stringMap(ctx, `SELECT name, value FROM slot_params WHERE slot_id = ? ORDER BY name`, s.GetId()); err != nil {
			return nil, err
		}
		req := &v1.RunRequest{SlotId: s.GetId()}
		err = d.sql.QueryRowContext(ctx, `SELECT source_id, repo, weight_group, runtime_id, install_id, name FROM slot_requests WHERE slot_id = ?`, s.GetId()).Scan(&req.SourceId, &req.Repo, &req.Group, &req.RuntimeId, &req.InstallId, &req.Name)
		switch {
		case err == sql.ErrNoRows:
		case err != nil:
			return nil, err
		default:
			if req.Params, err = d.stringMap(ctx, `SELECT name, value FROM slot_request_params WHERE slot_id = ? ORDER BY name`, s.GetId()); err != nil {
				return nil, err
			}
			s.Request = req
		}
	}
	return out, nil
}

// Removes a slot with its child rows, reporting existence
func (d *DB) DeleteSlot(ctx context.Context, id string) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM slots WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Inserts or replaces a route
func (d *DB) PutRoute(ctx context.Context, r *v1.Route) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO routes (name, instance_id, slot_id, endpoint, api, state, model, requests, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.GetName(), r.GetInstanceId(), r.GetSlotId(), r.GetEndpoint(), enumCol(r.GetApi()), enumCol(r.GetState()), r.GetModel(), int64(r.GetRequests()), stamp(r.GetUpdatedAt().AsTime()))
	return err
}

// Lists every route by name
func (d *DB) ListRoutes(ctx context.Context) ([]*v1.Route, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT name, instance_id, slot_id, endpoint, api, state, model, requests, updated_at FROM routes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*v1.Route
	for rows.Next() {
		r := &v1.Route{}
		var api, state, updated string
		var requests int64
		if err := rows.Scan(&r.Name, &r.InstanceId, &r.SlotId, &r.Endpoint, &api, &state, &r.Model, &requests, &updated); err != nil {
			return nil, err
		}
		r.Api = v1.ApiFlavor(enumVal(v1.ApiFlavor(0).Descriptor(), api))
		r.State = v1.RouteState(enumVal(v1.RouteState(0).Descriptor(), state))
		r.Requests = uint64(requests)
		r.UpdatedAt = timeVal(sql.NullString{String: updated, Valid: true})
		out = append(out, r)
	}
	return out, rows.Err()
}

// Removes a route by name
func (d *DB) DeleteRoute(ctx context.Context, name string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM routes WHERE name = ?`, name)
	return err
}

// Inserts or replaces a watch with params and known groups
func (d *DB) PutWatch(ctx context.Context, w *v1.Watch) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		exec := func(q string, args ...any) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		}
		id := w.GetId()
		// An upsert on id so duplicate repos trip the index
		if err := exec(`INSERT INTO watches (id, source_id, repo, revision, group_match, auto_pull, slot_id, runtime_id, last_commit, checked_at, created_at, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET source_id = excluded.source_id, repo = excluded.repo, revision = excluded.revision, group_match = excluded.group_match, auto_pull = excluded.auto_pull, slot_id = excluded.slot_id, runtime_id = excluded.runtime_id, last_commit = excluded.last_commit, checked_at = excluded.checked_at, created_at = excluded.created_at, error = excluded.error`,
			id, w.GetSourceId(), w.GetRepo(), w.GetRevision(), w.GetGroupMatch(), boolCol(w.GetAutoPull()), w.GetSlotId(), w.GetRuntimeId(), w.GetLastCommit(), timeCol(w.GetCheckedAt()), stamp(w.GetCreatedAt().AsTime()), w.GetError()); err != nil {
			return err
		}
		for _, table := range []string{"watch_params", "watch_groups"} {
			if err := exec(`DELETE FROM `+table+` WHERE watch_id = ?`, id); err != nil {
				return err
			}
		}
		if err := putMap(exec, `INSERT INTO watch_params (watch_id, name, value) VALUES (?, ?, ?)`, id, w.GetParams()); err != nil {
			return err
		}
		for i, g := range w.GetKnownGroups() {
			if err := exec(`INSERT INTO watch_groups (watch_id, position, group_name) VALUES (?, ?, ?)`, id, i, g); err != nil {
				return err
			}
		}
		return nil
	})
}

// Lists every watch oldest first
func (d *DB) ListWatches(ctx context.Context) ([]*v1.Watch, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT id, source_id, repo, revision, group_match, auto_pull, slot_id, runtime_id, last_commit, checked_at, created_at, error FROM watches ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	var out []*v1.Watch
	for rows.Next() {
		w := &v1.Watch{}
		var auto int
		var checked sql.NullString
		var created string
		if err := rows.Scan(&w.Id, &w.SourceId, &w.Repo, &w.Revision, &w.GroupMatch, &auto, &w.SlotId, &w.RuntimeId, &w.LastCommit, &checked, &created, &w.Error); err != nil {
			rows.Close()
			return nil, err
		}
		w.AutoPull = auto != 0
		w.CheckedAt = timeVal(checked)
		w.CreatedAt = timeVal(sql.NullString{String: created, Valid: true})
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, w := range out {
		if w.Params, err = d.stringMap(ctx, `SELECT name, value FROM watch_params WHERE watch_id = ? ORDER BY name`, w.GetId()); err != nil {
			return nil, err
		}
		if w.KnownGroups, err = d.strings(ctx, `SELECT group_name FROM watch_groups WHERE watch_id = ? ORDER BY position`, w.GetId()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Removes a watch and its findings
func (d *DB) DeleteWatch(ctx context.Context, id string) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM watches WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Inserts or replaces a finding
func (d *DB) PutFinding(ctx context.Context, f *v1.Finding) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO findings (id, watch_id, kind, repo, commit_id, group_name, detail, task_id, acknowledged, found_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.GetId(), f.GetWatchId(), enumCol(f.GetKind()), f.GetRepo(), f.GetCommit(), f.GetGroup(), f.GetDetail(), f.GetTaskId(), boolCol(f.GetAcknowledged()), stamp(f.GetFoundAt().AsTime()))
	return err
}

// Returns one finding by id
func (d *DB) GetFinding(ctx context.Context, id string) (*v1.Finding, error) {
	list, err := d.findings(ctx, `WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%w: finding %q", ErrNotFound, id)
	}
	return list[0], nil
}

// Lists findings newest first, all watches when watchID is empty
func (d *DB) ListFindings(ctx context.Context, watchID string, unackedOnly bool) ([]*v1.Finding, error) {
	where, args := ``, []any{}
	if watchID != "" {
		where, args = `WHERE watch_id = ?`, append(args, watchID)
	}
	if unackedOnly {
		if where == "" {
			where = `WHERE acknowledged = 0`
		} else {
			where += ` AND acknowledged = 0`
		}
	}
	return d.findings(ctx, where, args...)
}

// Drops the oldest acknowledged findings beyond keep
func (d *DB) PruneFindings(ctx context.Context, keep int) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM findings WHERE acknowledged = 1 AND id NOT IN (SELECT id FROM findings ORDER BY found_at DESC, id DESC LIMIT ?)`, keep)
	return err
}

func (d *DB) findings(ctx context.Context, where string, args ...any) ([]*v1.Finding, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT id, watch_id, kind, repo, commit_id, group_name, detail, task_id, acknowledged, found_at FROM findings `+where+` ORDER BY found_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*v1.Finding
	for rows.Next() {
		f := &v1.Finding{}
		var kind, found string
		var acked int
		if err := rows.Scan(&f.Id, &f.WatchId, &kind, &f.Repo, &f.Commit, &f.Group, &f.Detail, &f.TaskId, &acked, &found); err != nil {
			return nil, err
		}
		f.Kind = v1.FindingKind(enumVal(v1.FindingKind(0).Descriptor(), kind))
		f.Acknowledged = acked != 0
		f.FoundAt = timeVal(sql.NullString{String: found, Valid: true})
		out = append(out, f)
	}
	return out, rows.Err()
}
