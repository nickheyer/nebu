package db

import (
	"context"
	"database/sql"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Inserts or replaces an instance with every child row
func (d *DB) PutInstance(ctx context.Context, in *v1.Instance) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		exec := func(q string, args ...any) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		}
		id := in.GetId()
		if err := exec(`INSERT OR REPLACE INTO instances (id, name, source_id, repo, weight_group, runtime_id, install_id, endpoint, state, pid, error, task_id, desired_running, created_at, ready_at, stopped_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.GetName(), in.GetSourceId(), in.GetRepo(), in.GetGroup(), in.GetRuntimeId(), in.GetInstallId(), in.GetEndpoint(), enumCol(in.GetState()), in.GetPid(), in.GetError(), in.GetTaskId(), boolCol(in.GetDesiredRunning()), stamp(in.GetCreatedAt().AsTime()), timeCol(in.GetReadyAt()), timeCol(in.GetStoppedAt())); err != nil {
			return err
		}
		for _, table := range []string{"instance_params", "instance_command", "instance_requests", "instance_request_params", "instance_plans", "instance_plan_pools", "instance_plan_placements", "instance_plan_params", "instance_measurements", "instance_triage", "instance_triage_fixes"} {
			if err := exec(`DELETE FROM `+table+` WHERE instance_id = ?`, id); err != nil {
				return err
			}
		}
		if err := putMap(exec, `INSERT INTO instance_params (instance_id, name, value) VALUES (?, ?, ?)`, id, in.GetParams()); err != nil {
			return err
		}
		for i, arg := range in.GetCommand() {
			if err := exec(`INSERT INTO instance_command (instance_id, position, arg) VALUES (?, ?, ?)`, id, i, arg); err != nil {
				return err
			}
		}
		if req := in.GetRequest(); req != nil {
			if err := exec(`INSERT INTO instance_requests (instance_id, source_id, repo, weight_group, runtime_id, install_id, name) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				id, req.GetSourceId(), req.GetRepo(), req.GetGroup(), req.GetRuntimeId(), req.GetInstallId(), req.GetName()); err != nil {
				return err
			}
			if err := putMap(exec, `INSERT INTO instance_request_params (instance_id, name, value) VALUES (?, ?, ?)`, id, req.GetParams()); err != nil {
				return err
			}
		}
		if plan := in.GetPlan(); plan != nil {
			if err := exec(`INSERT INTO instance_plans (instance_id, verdict, weights_bytes, cache_bytes, overhead_bytes, detail) VALUES (?, ?, ?, ?, ?, ?)`,
				id, enumCol(plan.GetVerdict()), int64(plan.GetWeightsBytes()), int64(plan.GetCacheBytes()), int64(plan.GetOverheadBytes()), plan.GetDetail()); err != nil {
				return err
			}
			for i, p := range plan.GetPools() {
				if err := exec(`INSERT INTO instance_plan_pools (instance_id, position, pool_id, kind, used_bytes, capacity_bytes) VALUES (?, ?, ?, ?, ?, ?)`,
					id, i, p.GetPoolId(), enumCol(p.GetKind()), int64(p.GetUsedBytes()), int64(p.GetCapacityBytes())); err != nil {
					return err
				}
			}
			for i, p := range plan.GetPlacements() {
				if err := exec(`INSERT INTO instance_plan_placements (instance_id, position, kind, pool_id, bytes, count) VALUES (?, ?, ?, ?, ?, ?)`,
					id, i, enumCol(p.GetKind()), p.GetPoolId(), int64(p.GetBytes()), p.GetCount()); err != nil {
					return err
				}
			}
			if err := putMap(exec, `INSERT INTO instance_plan_params (instance_id, name, value) VALUES (?, ?, ?)`, id, plan.GetParams()); err != nil {
				return err
			}
		}
		for i, m := range in.GetMeasurements() {
			if err := exec(`INSERT INTO instance_measurements (instance_id, position, key, bytes, line) VALUES (?, ?, ?, ?, ?)`, id, i, m.GetKey(), int64(m.GetBytes()), m.GetLine()); err != nil {
				return err
			}
		}
		for i, h := range in.GetTriage() {
			if err := exec(`INSERT INTO instance_triage (instance_id, position, rule_id, summary, hint, line) VALUES (?, ?, ?, ?, ?, ?)`, id, i, h.GetId(), h.GetSummary(), h.GetHint(), h.GetLine()); err != nil {
				return err
			}
			for name, value := range h.GetFix() {
				if err := exec(`INSERT INTO instance_triage_fixes (instance_id, position, name, value) VALUES (?, ?, ?, ?)`, id, i, name, value); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Lists every instance oldest first with all child rows
func (d *DB) ListInstances(ctx context.Context) ([]*v1.Instance, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT id, name, source_id, repo, weight_group, runtime_id, install_id, endpoint, state, pid, error, task_id, desired_running, created_at, ready_at, stopped_at FROM instances ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	var out []*v1.Instance
	for rows.Next() {
		in := &v1.Instance{}
		var state, created string
		var ready, stopped sql.NullString
		var desired int
		if err := rows.Scan(&in.Id, &in.Name, &in.SourceId, &in.Repo, &in.Group, &in.RuntimeId, &in.InstallId, &in.Endpoint, &state, &in.Pid, &in.Error, &in.TaskId, &desired, &created, &ready, &stopped); err != nil {
			rows.Close()
			return nil, err
		}
		in.State = v1.InstanceState(enumVal(v1.InstanceState(0).Descriptor(), state))
		in.DesiredRunning = desired != 0
		in.CreatedAt = timeVal(sql.NullString{String: created, Valid: true})
		in.ReadyAt = timeVal(ready)
		in.StoppedAt = timeVal(stopped)
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, in := range out {
		if err := d.fillInstance(ctx, in); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (d *DB) fillInstance(ctx context.Context, in *v1.Instance) error {
	id := in.GetId()
	var err error
	if in.Params, err = d.stringMap(ctx, `SELECT name, value FROM instance_params WHERE instance_id = ? ORDER BY name`, id); err != nil {
		return err
	}
	if in.Command, err = d.strings(ctx, `SELECT arg FROM instance_command WHERE instance_id = ? ORDER BY position`, id); err != nil {
		return err
	}
	req := &v1.RunRequest{}
	err = d.sql.QueryRowContext(ctx, `SELECT source_id, repo, weight_group, runtime_id, install_id, name FROM instance_requests WHERE instance_id = ?`, id).Scan(&req.SourceId, &req.Repo, &req.Group, &req.RuntimeId, &req.InstallId, &req.Name)
	switch {
	case err == sql.ErrNoRows:
	case err != nil:
		return err
	default:
		if req.Params, err = d.stringMap(ctx, `SELECT name, value FROM instance_request_params WHERE instance_id = ? ORDER BY name`, id); err != nil {
			return err
		}
		in.Request = req
	}
	plan := &v1.MemoryPlan{}
	var verdict string
	var weights, cache, overhead int64
	err = d.sql.QueryRowContext(ctx, `SELECT verdict, weights_bytes, cache_bytes, overhead_bytes, detail FROM instance_plans WHERE instance_id = ?`, id).Scan(&verdict, &weights, &cache, &overhead, &plan.Detail)
	switch {
	case err == sql.ErrNoRows:
	case err != nil:
		return err
	default:
		plan.Verdict = v1.FitVerdict(enumVal(v1.FitVerdict(0).Descriptor(), verdict))
		plan.WeightsBytes, plan.CacheBytes, plan.OverheadBytes = uint64(weights), uint64(cache), uint64(overhead)
		if err := d.each(ctx, `SELECT pool_id, kind, used_bytes, capacity_bytes FROM instance_plan_pools WHERE instance_id = ? ORDER BY position`, func(rows *sql.Rows) error {
			p := &v1.PoolUsage{}
			var kind string
			var used, capacity int64
			if err := rows.Scan(&p.PoolId, &kind, &used, &capacity); err != nil {
				return err
			}
			p.Kind = v1.PoolKind(enumVal(v1.PoolKind(0).Descriptor(), kind))
			p.UsedBytes, p.CapacityBytes = uint64(used), uint64(capacity)
			plan.Pools = append(plan.Pools, p)
			return nil
		}, id); err != nil {
			return err
		}
		if err := d.each(ctx, `SELECT kind, pool_id, bytes, count FROM instance_plan_placements WHERE instance_id = ? ORDER BY position`, func(rows *sql.Rows) error {
			p := &v1.Placement{}
			var kind string
			var bytes int64
			if err := rows.Scan(&kind, &p.PoolId, &bytes, &p.Count); err != nil {
				return err
			}
			p.Kind = v1.TensorGroupKind(enumVal(v1.TensorGroupKind(0).Descriptor(), kind))
			p.Bytes = uint64(bytes)
			plan.Placements = append(plan.Placements, p)
			return nil
		}, id); err != nil {
			return err
		}
		if plan.Params, err = d.stringMap(ctx, `SELECT name, value FROM instance_plan_params WHERE instance_id = ? ORDER BY name`, id); err != nil {
			return err
		}
		in.Plan = plan
	}
	if err := d.each(ctx, `SELECT key, bytes, line FROM instance_measurements WHERE instance_id = ? ORDER BY position`, func(rows *sql.Rows) error {
		m := &v1.Measurement{}
		var bytes int64
		if err := rows.Scan(&m.Key, &bytes, &m.Line); err != nil {
			return err
		}
		m.Bytes = uint64(bytes)
		in.Measurements = append(in.Measurements, m)
		return nil
	}, id); err != nil {
		return err
	}
	var positions []int
	if err := d.each(ctx, `SELECT position, rule_id, summary, hint, line FROM instance_triage WHERE instance_id = ? ORDER BY position`, func(rows *sql.Rows) error {
		h := &v1.TriageHit{}
		var pos int
		if err := rows.Scan(&pos, &h.Id, &h.Summary, &h.Hint, &h.Line); err != nil {
			return err
		}
		positions = append(positions, pos)
		in.Triage = append(in.Triage, h)
		return nil
	}, id); err != nil {
		return err
	}
	for i, h := range in.Triage {
		fix, err := d.stringMap(ctx, `SELECT name, value FROM instance_triage_fixes WHERE instance_id = ? AND position = ? ORDER BY name`, id, positions[i])
		if err != nil {
			return err
		}
		if len(fix) > 0 {
			h.Fix = fix
		}
	}
	return nil
}

// Removes an instance and its child rows
func (d *DB) DeleteInstance(ctx context.Context, id string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM instances WHERE id = ?`, id)
	return err
}

func (d *DB) strings(ctx context.Context, query string, args ...any) ([]string, error) {
	var out []string
	err := d.each(ctx, query, func(rows *sql.Rows) error {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		out = append(out, s)
		return nil
	}, args...)
	return out, err
}

func (d *DB) each(ctx context.Context, query string, fn func(*sql.Rows) error, args ...any) error {
	rows, err := d.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
