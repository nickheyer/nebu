package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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
		p := s.GetPolicy()
		if err := exec(`INSERT OR REPLACE INTO slots (id, name, description, runtime_id, memory_bytes, instance_id, state, error, task_id, created_at, updated_at, max_in_flight, requests_per_second, burst, request_timeout_ms, upstream_timeout_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, s.GetName(), s.GetDescription(), s.GetRuntimeId(), int64(s.GetMemoryBytes()), s.GetInstanceId(), enumCol(s.GetState()), s.GetError(), s.GetTaskId(), stamp(s.GetCreatedAt().AsTime()), stamp(s.GetUpdatedAt().AsTime()),
			p.GetMaxInFlight(), p.GetRequestsPerSecond(), p.GetBurst(), p.GetRequestTimeoutMs(), p.GetUpstreamTimeoutMs()); err != nil {
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
			if err := exec(`INSERT INTO slot_requests (slot_id, source_id, repo, weight_group, runtime_id, install_id, name, profile_id, force) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, req.GetSourceId(), req.GetRepo(), req.GetGroup(), req.GetRuntimeId(), req.GetInstallId(), req.GetName(), req.GetProfileId(), boolCol(req.GetForce())); err != nil {
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
	rows, err := d.sql.QueryContext(ctx, `SELECT id, name, description, runtime_id, memory_bytes, instance_id, state, error, task_id, created_at, updated_at, max_in_flight, requests_per_second, burst, request_timeout_ms, upstream_timeout_ms FROM slots ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var out []*v1.Slot
	for rows.Next() {
		s := &v1.Slot{}
		p := &v1.Policy{}
		var memory int64
		var state, created, updated string
		if err := rows.Scan(&s.Id, &s.Name, &s.Description, &s.RuntimeId, &memory, &s.InstanceId, &state, &s.Error, &s.TaskId, &created, &updated, &p.MaxInFlight, &p.RequestsPerSecond, &p.Burst, &p.RequestTimeoutMs, &p.UpstreamTimeoutMs); err != nil {
			rows.Close()
			return nil, err
		}
		// A slot that inherits everything carries no policy, as it was written
		if p.GetMaxInFlight()+p.GetBurst()+p.GetRequestTimeoutMs()+p.GetUpstreamTimeoutMs() > 0 || p.GetRequestsPerSecond() > 0 {
			s.Policy = p
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
		var force int
		err = d.sql.QueryRowContext(ctx, `SELECT source_id, repo, weight_group, runtime_id, install_id, name, profile_id, force FROM slot_requests WHERE slot_id = ?`, s.GetId()).Scan(&req.SourceId, &req.Repo, &req.Group, &req.RuntimeId, &req.InstallId, &req.Name, &req.ProfileId, &force)
		switch {
		case err == sql.ErrNoRows:
		case err != nil:
			return nil, err
		default:
			req.Force = force != 0
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
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO routes (name, instance_id, slot_id, endpoint, api, state, model, requests, updated_at, served) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.GetName(), r.GetInstanceId(), r.GetSlotId(), r.GetEndpoint(), enumCol(r.GetApi()), enumCol(r.GetState()), r.GetModel(), int64(r.GetRequests()), stamp(r.GetUpdatedAt().AsTime()), r.GetServed())
	return err
}

// Lists every route by name
func (d *DB) ListRoutes(ctx context.Context) ([]*v1.Route, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT name, instance_id, slot_id, endpoint, api, state, model, requests, updated_at, served FROM routes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*v1.Route
	for rows.Next() {
		r := &v1.Route{}
		var api, state, updated string
		var requests int64
		if err := rows.Scan(&r.Name, &r.InstanceId, &r.SlotId, &r.Endpoint, &api, &state, &r.Model, &requests, &updated, &r.Served); err != nil {
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
		if err := exec(`INSERT INTO watches (id, source_id, repo, revision, group_match, auto_pull, slot_id, runtime_id, profile_id, last_commit, checked_at, created_at, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET source_id = excluded.source_id, repo = excluded.repo, revision = excluded.revision, group_match = excluded.group_match, auto_pull = excluded.auto_pull, slot_id = excluded.slot_id, runtime_id = excluded.runtime_id, profile_id = excluded.profile_id, last_commit = excluded.last_commit, checked_at = excluded.checked_at, created_at = excluded.created_at, error = excluded.error`,
			id, w.GetSourceId(), w.GetRepo(), w.GetRevision(), w.GetGroupMatch(), boolCol(w.GetAutoPull()), w.GetSlotId(), w.GetRuntimeId(), w.GetProfileId(), w.GetLastCommit(), timeCol(w.GetCheckedAt()), stamp(w.GetCreatedAt().AsTime()), w.GetError()); err != nil {
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
	rows, err := d.sql.QueryContext(ctx, `SELECT id, source_id, repo, revision, group_match, auto_pull, slot_id, runtime_id, profile_id, last_commit, checked_at, created_at, error FROM watches ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	var out []*v1.Watch
	for rows.Next() {
		w := &v1.Watch{}
		var auto int
		var checked sql.NullString
		var created string
		if err := rows.Scan(&w.Id, &w.SourceId, &w.Repo, &w.Revision, &w.GroupMatch, &auto, &w.SlotId, &w.RuntimeId, &w.ProfileId, &w.LastCommit, &checked, &created, &w.Error); err != nil {
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
	if _, err := d.sql.ExecContext(ctx, `DELETE FROM findings WHERE watch_id = ?`, id); err != nil {
		return false, err
	}
	res, err := d.sql.ExecContext(ctx, `DELETE FROM watches WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Inserts or replaces a want with its params
func (d *DB) PutWant(ctx context.Context, w *v1.Want) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		exec := func(q string, args ...any) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		}
		id := w.GetId()
		if err := exec(`INSERT OR REPLACE INTO wants (id, query, kind, source_id, group_match, format_id, auto_pull, slot_id, runtime_id, profile_id, found_source_id, found_repo, found_group, task_id, satisfied, checked_at, created_at, error, swap_task_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, w.GetQuery(), enumCol(w.GetKind()), w.GetSourceId(), w.GetGroupMatch(), w.GetFormatId(), boolCol(w.GetAutoPull()), w.GetSlotId(), w.GetRuntimeId(), w.GetProfileId(), w.GetFoundSourceId(), w.GetFoundRepo(), w.GetFoundGroup(), w.GetTaskId(), boolCol(w.GetSatisfied()), timeCol(w.GetCheckedAt()), stamp(w.GetCreatedAt().AsTime()), w.GetError(), w.GetSwapTaskId()); err != nil {
			return err
		}
		if err := exec(`DELETE FROM want_params WHERE want_id = ?`, id); err != nil {
			return err
		}
		return putMap(exec, `INSERT INTO want_params (want_id, name, value) VALUES (?, ?, ?)`, id, w.GetParams())
	})
}

// Lists every want oldest first
func (d *DB) ListWants(ctx context.Context) ([]*v1.Want, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT id, query, kind, source_id, group_match, format_id, auto_pull, slot_id, runtime_id, profile_id, found_source_id, found_repo, found_group, task_id, satisfied, checked_at, created_at, error, swap_task_id FROM wants ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	var out []*v1.Want
	for rows.Next() {
		w := &v1.Want{}
		var kind, created string
		var auto, satisfied int
		var checked sql.NullString
		if err := rows.Scan(&w.Id, &w.Query, &kind, &w.SourceId, &w.GroupMatch, &w.FormatId, &auto, &w.SlotId, &w.RuntimeId, &w.ProfileId, &w.FoundSourceId, &w.FoundRepo, &w.FoundGroup, &w.TaskId, &satisfied, &checked, &created, &w.Error, &w.SwapTaskId); err != nil {
			rows.Close()
			return nil, err
		}
		w.Kind = v1.SourceKind(enumVal(v1.SourceKind(0).Descriptor(), kind))
		w.AutoPull, w.Satisfied = auto != 0, satisfied != 0
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
		if w.Params, err = d.stringMap(ctx, `SELECT name, value FROM want_params WHERE want_id = ? ORDER BY name`, w.GetId()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Removes a want and its findings, reporting existence
func (d *DB) DeleteWant(ctx context.Context, id string) (bool, error) {
	if _, err := d.sql.ExecContext(ctx, `DELETE FROM findings WHERE want_id = ?`, id); err != nil {
		return false, err
	}
	res, err := d.sql.ExecContext(ctx, `DELETE FROM wants WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Inserts or replaces a finding
func (d *DB) PutFinding(ctx context.Context, f *v1.Finding) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO findings (id, watch_id, want_id, source_id, kind, repo, commit_id, group_name, detail, task_id, acknowledged, found_at, swap_task_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.GetId(), f.GetWatchId(), f.GetWantId(), f.GetSourceId(), enumCol(f.GetKind()), f.GetRepo(), f.GetCommit(), f.GetGroup(), f.GetDetail(), f.GetTaskId(), boolCol(f.GetAcknowledged()), stamp(f.GetFoundAt().AsTime()), f.GetSwapTaskId())
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

// Lists findings newest first, narrowed to a watch or a want when named
func (d *DB) ListFindings(ctx context.Context, watchID, wantID string, unackedOnly bool) ([]*v1.Finding, error) {
	var clauses []string
	var args []any
	if watchID != "" {
		clauses, args = append(clauses, `watch_id = ?`), append(args, watchID)
	}
	if wantID != "" {
		clauses, args = append(clauses, `want_id = ?`), append(args, wantID)
	}
	if unackedOnly {
		clauses = append(clauses, `acknowledged = 0`)
	}
	where := ""
	if len(clauses) > 0 {
		where = `WHERE ` + strings.Join(clauses, ` AND `)
	}
	return d.findings(ctx, where, args...)
}

// Drops the oldest acknowledged findings beyond keep
// Drops acknowledged findings past the newest keep, returning what went so the stream can say
func (d *DB) PruneFindings(ctx context.Context, keep int) ([]*v1.Finding, error) {
	gone, err := d.findings(ctx, `WHERE acknowledged = 1 AND id NOT IN (SELECT id FROM findings ORDER BY found_at DESC, id DESC LIMIT ?)`, keep)
	if err != nil || len(gone) == 0 {
		return nil, err
	}
	for _, f := range gone {
		if _, err := d.sql.ExecContext(ctx, `DELETE FROM findings WHERE id = ?`, f.GetId()); err != nil {
			return nil, err
		}
	}
	return gone, nil
}

func (d *DB) findings(ctx context.Context, where string, args ...any) ([]*v1.Finding, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT id, watch_id, want_id, source_id, kind, repo, commit_id, group_name, detail, task_id, acknowledged, found_at, swap_task_id FROM findings `+where+` ORDER BY found_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*v1.Finding
	for rows.Next() {
		f := &v1.Finding{}
		var kind, found string
		var acked int
		if err := rows.Scan(&f.Id, &f.WatchId, &f.WantId, &f.SourceId, &kind, &f.Repo, &f.Commit, &f.Group, &f.Detail, &f.TaskId, &acked, &found, &f.SwapTaskId); err != nil {
			return nil, err
		}
		f.Kind = v1.FindingKind(enumVal(v1.FindingKind(0).Descriptor(), kind))
		f.Acknowledged = acked != 0
		f.FoundAt = timeVal(sql.NullString{String: found, Valid: true})
		out = append(out, f)
	}
	return out, rows.Err()
}
