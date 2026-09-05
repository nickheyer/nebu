package db

import (
	"context"
	"database/sql"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Inserts or replaces an instance with every child row
func (d *DB) PutInstance(ctx context.Context, in *v1.Instance) error {
	return d.tx(ctx, func(exec execFn) error {
		id := in.GetId()
		if err := exec(`INSERT OR REPLACE INTO instances (id, name, source_id, repo, weight_group, runtime_id, install_id, endpoint, state, pid, error, task_id, desired_running, created_at, ready_at, stopped_at, slot_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.GetName(), in.GetSourceId(), in.GetRepo(), in.GetGroup(), in.GetRuntimeId(), in.GetInstallId(), in.GetEndpoint(), enumCol(in.GetState()), in.GetPid(), in.GetError(), in.GetTaskId(), boolCol(in.GetDesiredRunning()), stamp(in.GetCreatedAt().AsTime()), timeCol(in.GetReadyAt()), timeCol(in.GetStoppedAt()), in.GetSlotId()); err != nil {
			return err
		}
		if err := clearChildren(exec, "instance_id", id, "instance_params", "instance_command", "instance_requests", "instance_request_params", "instance_plans", "instance_plan_pools", "instance_plan_placements", "instance_plan_params", "instance_measurements", "instance_triage", "instance_triage_fixes"); err != nil {
			return err
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
			if err := exec(`INSERT INTO instance_requests (instance_id, source_id, repo, weight_group, runtime_id, install_id, name, slot_id, profile_id, force) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, req.GetSourceId(), req.GetRepo(), req.GetGroup(), req.GetRuntimeId(), req.GetInstallId(), req.GetName(), req.GetSlotId(), req.GetProfileId(), boolCol(req.GetForce())); err != nil {
				return err
			}
			if err := putMap(exec, `INSERT INTO instance_request_params (instance_id, name, value) VALUES (?, ?, ?)`, id, req.GetParams()); err != nil {
				return err
			}
		}
		if plan := in.GetPlan(); plan != nil {
			if err := exec(`INSERT INTO instance_plans (instance_id, verdict, weights_bytes, cache_bytes, overhead_bytes, detail, overhead_delta) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				id, enumCol(plan.GetVerdict()), int64(plan.GetWeightsBytes()), int64(plan.GetCacheBytes()), int64(plan.GetOverheadBytes()), plan.GetDetail(), plan.GetOverheadDelta()); err != nil {
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
	out, err := list(ctx, d, `SELECT id, name, source_id, repo, weight_group, runtime_id, install_id, endpoint, state, pid, error, task_id, desired_running, created_at, ready_at, stopped_at, slot_id FROM instances ORDER BY created_at, id`, func(rows *sql.Rows) (*v1.Instance, error) {
		in := &v1.Instance{}
		return in, rows.Scan(&in.Id, &in.Name, &in.SourceId, &in.Repo, &in.Group, &in.RuntimeId, &in.InstallId, &in.Endpoint, enumAt[v1.InstanceState]{&in.State}, &in.Pid, &in.Error, &in.TaskId, (*flag)(&in.DesiredRunning), at{&in.CreatedAt}, at{&in.ReadyAt}, at{&in.StoppedAt}, &in.SlotId)
	})
	if err != nil {
		return nil, err
	}
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
	requests, err := list(ctx, d, `SELECT source_id, repo, weight_group, runtime_id, install_id, name, slot_id, profile_id, force FROM instance_requests WHERE instance_id = ?`, func(rows *sql.Rows) (*v1.RunRequest, error) {
		req := &v1.RunRequest{}
		return req, rows.Scan(&req.SourceId, &req.Repo, &req.Group, &req.RuntimeId, &req.InstallId, &req.Name, &req.SlotId, &req.ProfileId, (*flag)(&req.Force))
	}, id)
	if err != nil {
		return err
	}
	if len(requests) > 0 {
		in.Request = requests[0]
		if in.Request.Params, err = d.stringMap(ctx, `SELECT name, value FROM instance_request_params WHERE instance_id = ? ORDER BY name`, id); err != nil {
			return err
		}
	}
	plans, err := list(ctx, d, `SELECT verdict, weights_bytes, cache_bytes, overhead_bytes, detail, overhead_delta FROM instance_plans WHERE instance_id = ?`, func(rows *sql.Rows) (*v1.MemoryPlan, error) {
		plan := &v1.MemoryPlan{}
		return plan, rows.Scan(enumAt[v1.FitVerdict]{&plan.Verdict}, &plan.WeightsBytes, &plan.CacheBytes, &plan.OverheadBytes, &plan.Detail, &plan.OverheadDelta)
	}, id)
	if err != nil {
		return err
	}
	if len(plans) > 0 {
		plan := plans[0]
		if plan.Pools, err = list(ctx, d, `SELECT pool_id, kind, used_bytes, capacity_bytes FROM instance_plan_pools WHERE instance_id = ? ORDER BY position`, func(rows *sql.Rows) (*v1.PoolUsage, error) {
			p := &v1.PoolUsage{}
			return p, rows.Scan(&p.PoolId, enumAt[v1.PoolKind]{&p.Kind}, &p.UsedBytes, &p.CapacityBytes)
		}, id); err != nil {
			return err
		}
		if plan.Placements, err = list(ctx, d, `SELECT kind, pool_id, bytes, count FROM instance_plan_placements WHERE instance_id = ? ORDER BY position`, func(rows *sql.Rows) (*v1.Placement, error) {
			p := &v1.Placement{}
			return p, rows.Scan(enumAt[v1.TensorGroupKind]{&p.Kind}, &p.PoolId, &p.Bytes, &p.Count)
		}, id); err != nil {
			return err
		}
		if plan.Params, err = d.stringMap(ctx, `SELECT name, value FROM instance_plan_params WHERE instance_id = ? ORDER BY name`, id); err != nil {
			return err
		}
		in.Plan = plan
	}
	if in.Measurements, err = list(ctx, d, `SELECT key, bytes, line FROM instance_measurements WHERE instance_id = ? ORDER BY position`, func(rows *sql.Rows) (*v1.Measurement, error) {
		m := &v1.Measurement{}
		return m, rows.Scan(&m.Key, &m.Bytes, &m.Line)
	}, id); err != nil {
		return err
	}
	var positions []int
	if in.Triage, err = list(ctx, d, `SELECT position, rule_id, summary, hint, line FROM instance_triage WHERE instance_id = ? ORDER BY position`, func(rows *sql.Rows) (*v1.TriageHit, error) {
		h := &v1.TriageHit{}
		var pos int
		err := rows.Scan(&pos, &h.Id, &h.Summary, &h.Hint, &h.Line)
		positions = append(positions, pos)
		return h, err
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
	_, err := d.del(ctx, "instances", "id", id)
	return err
}
