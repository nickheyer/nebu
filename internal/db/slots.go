package db

import (
	"context"
	"database/sql"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Inserts or replaces a slot with devices, params, and request
func (d *DB) PutSlot(ctx context.Context, s *v1.Slot) error {
	return d.tx(ctx, func(exec execFn) error {
		id := s.GetId()
		p := s.GetPolicy()
		if err := exec(`INSERT OR REPLACE INTO slots (id, name, description, runtime_id, memory_bytes, instance_id, state, error, task_id, created_at, updated_at, max_in_flight, requests_per_second, burst, request_timeout_ms, upstream_timeout_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, s.GetName(), s.GetDescription(), s.GetRuntimeId(), int64(s.GetMemoryBytes()), s.GetInstanceId(), enumCol(s.GetState()), s.GetError(), s.GetTaskId(), stamp(s.GetCreatedAt().AsTime()), stamp(s.GetUpdatedAt().AsTime()),
			p.GetMaxInFlight(), p.GetRequestsPerSecond(), p.GetBurst(), p.GetRequestTimeoutMs(), p.GetUpstreamTimeoutMs()); err != nil {
			return err
		}
		if err := clearChildren(exec, "slot_id", id, "slot_devices", "slot_params", "slot_requests", "slot_request_params"); err != nil {
			return err
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
	out, err := list(ctx, d, `SELECT id, name, description, runtime_id, memory_bytes, instance_id, state, error, task_id, created_at, updated_at, max_in_flight, requests_per_second, burst, request_timeout_ms, upstream_timeout_ms FROM slots ORDER BY name`, func(rows *sql.Rows) (*v1.Slot, error) {
		s := &v1.Slot{}
		p := &v1.Policy{}
		err := rows.Scan(&s.Id, &s.Name, &s.Description, &s.RuntimeId, &s.MemoryBytes, &s.InstanceId, enumAt[v1.SlotState]{&s.State}, &s.Error, &s.TaskId, at{&s.CreatedAt}, at{&s.UpdatedAt}, &p.MaxInFlight, &p.RequestsPerSecond, &p.Burst, &p.RequestTimeoutMs, &p.UpstreamTimeoutMs)
		// A slot that inherits everything carries no policy, as it was written
		if p.GetMaxInFlight()+p.GetBurst()+p.GetRequestTimeoutMs()+p.GetUpstreamTimeoutMs() > 0 || p.GetRequestsPerSecond() > 0 {
			s.Policy = p
		}
		return s, err
	})
	if err != nil {
		return nil, err
	}
	for _, s := range out {
		if s.DeviceIds, err = d.strings(ctx, `SELECT device_id FROM slot_devices WHERE slot_id = ? ORDER BY position`, s.GetId()); err != nil {
			return nil, err
		}
		if s.Params, err = d.stringMap(ctx, `SELECT name, value FROM slot_params WHERE slot_id = ? ORDER BY name`, s.GetId()); err != nil {
			return nil, err
		}
		requests, err := list(ctx, d, `SELECT source_id, repo, weight_group, runtime_id, install_id, name, profile_id, force FROM slot_requests WHERE slot_id = ?`, func(rows *sql.Rows) (*v1.RunRequest, error) {
			req := &v1.RunRequest{SlotId: s.GetId()}
			return req, rows.Scan(&req.SourceId, &req.Repo, &req.Group, &req.RuntimeId, &req.InstallId, &req.Name, &req.ProfileId, (*flag)(&req.Force))
		}, s.GetId())
		if err != nil {
			return nil, err
		}
		if len(requests) > 0 {
			s.Request = requests[0]
			if s.Request.Params, err = d.stringMap(ctx, `SELECT name, value FROM slot_request_params WHERE slot_id = ? ORDER BY name`, s.GetId()); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// Removes a slot with its child rows, reporting existence
func (d *DB) DeleteSlot(ctx context.Context, id string) (bool, error) {
	return d.del(ctx, "slots", "id", id)
}

// Inserts or replaces a route
func (d *DB) PutRoute(ctx context.Context, r *v1.Route) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO routes (name, instance_id, slot_id, endpoint, api, state, model, requests, updated_at, served) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.GetName(), r.GetInstanceId(), r.GetSlotId(), r.GetEndpoint(), enumCol(r.GetApi()), enumCol(r.GetState()), r.GetModel(), int64(r.GetRequests()), stamp(r.GetUpdatedAt().AsTime()), r.GetServed())
	return err
}

// Lists every route by name
func (d *DB) ListRoutes(ctx context.Context) ([]*v1.Route, error) {
	return list(ctx, d, `SELECT name, instance_id, slot_id, endpoint, api, state, model, requests, updated_at, served FROM routes ORDER BY name`, func(rows *sql.Rows) (*v1.Route, error) {
		r := &v1.Route{}
		return r, rows.Scan(&r.Name, &r.InstanceId, &r.SlotId, &r.Endpoint, enumAt[v1.ApiFlavor]{&r.Api}, enumAt[v1.RouteState]{&r.State}, &r.Model, &r.Requests, at{&r.UpdatedAt}, &r.Served)
	})
}

// Removes a route by name
func (d *DB) DeleteRoute(ctx context.Context, name string) error {
	_, err := d.del(ctx, "routes", "name", name)
	return err
}
