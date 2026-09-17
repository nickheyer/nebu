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
		if err := exec(`INSERT OR REPLACE INTO slots (id, name, position, placement, runtime_id, memory_bytes, instance_id, state, error, task_id, created_at, updated_at, max_in_flight, requests_per_second, burst, request_timeout_ms, upstream_timeout_ms, system_messages) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, s.GetName(), s.GetPosition(), enumCol(s.GetPlacement()), s.GetRuntimeId(), int64(s.GetMemoryBytes()), s.GetInstanceId(), enumCol(s.GetState()), s.GetError(), s.GetTaskId(), stamp(s.GetCreatedAt().AsTime()), stamp(s.GetUpdatedAt().AsTime()),
			p.GetMaxInFlight(), p.GetRequestsPerSecond(), p.GetBurst(), p.GetRequestTimeoutMs(), p.GetUpstreamTimeoutMs(), profileCol(s.GetProfile())); err != nil {
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
			if err := exec(`INSERT INTO slot_requests (slot_id, source_id, repo, weight_group, runtime_id, install_id, name, force) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				id, req.GetSourceId(), req.GetRepo(), req.GetGroup(), req.GetRuntimeId(), req.GetInstallId(), req.GetName(), boolCol(req.GetForce())); err != nil {
				return err
			}
			if err := putMap(exec, `INSERT INTO slot_request_params (slot_id, name, value) VALUES (?, ?, ?)`, id, req.GetParams()); err != nil {
				return err
			}
		}
		return nil
	})
}

// Lists every slot by position, then name
func (d *DB) ListSlots(ctx context.Context) ([]*v1.Slot, error) {
	out, err := list(ctx, d, `SELECT id, name, position, placement, runtime_id, memory_bytes, instance_id, state, error, task_id, created_at, updated_at, max_in_flight, requests_per_second, burst, request_timeout_ms, upstream_timeout_ms, system_messages FROM slots ORDER BY position, name`, func(rows *sql.Rows) (*v1.Slot, error) {
		s := &v1.Slot{}
		p := &v1.Policy{}
		var profile profileAt
		err := rows.Scan(&s.Id, &s.Name, &s.Position, enumAt[v1.Placement]{&s.Placement}, &s.RuntimeId, &s.MemoryBytes, &s.InstanceId, enumAt[v1.SlotState]{&s.State}, &s.Error, &s.TaskId, at{&s.CreatedAt}, at{&s.UpdatedAt}, &p.MaxInFlight, &p.RequestsPerSecond, &p.Burst, &p.RequestTimeoutMs, &p.UpstreamTimeoutMs, &profile)
		// A slot that inherits everything carries no policy, as it was written
		if p.GetMaxInFlight()+p.GetBurst()+p.GetRequestTimeoutMs()+p.GetUpstreamTimeoutMs() > 0 || p.GetRequestsPerSecond() > 0 {
			s.Policy = p
		}
		s.Profile = profile.p
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
		requests, err := list(ctx, d, `SELECT source_id, repo, weight_group, runtime_id, install_id, name, force FROM slot_requests WHERE slot_id = ?`, func(rows *sql.Rows) (*v1.RunRequest, error) {
			req := &v1.RunRequest{SlotId: s.GetId()}
			return req, rows.Scan(&req.SourceId, &req.Repo, &req.Group, &req.RuntimeId, &req.InstallId, &req.Name, (*flag)(&req.Force))
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
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO routes (name, instance_id, slot_id, endpoint, api, state, model, requests, updated_at, served, system_messages) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.GetName(), r.GetInstanceId(), r.GetSlotId(), r.GetEndpoint(), enumCol(r.GetApi()), enumCol(r.GetState()), r.GetModel(), int64(r.GetRequests()), stamp(r.GetUpdatedAt().AsTime()), r.GetServed(), profileCol(r.GetProfile()))
	return err
}

// Lists every route by name
func (d *DB) ListRoutes(ctx context.Context) ([]*v1.Route, error) {
	return list(ctx, d, `SELECT name, instance_id, slot_id, endpoint, api, state, model, requests, updated_at, served, system_messages FROM routes ORDER BY name`, func(rows *sql.Rows) (*v1.Route, error) {
		r := &v1.Route{}
		var profile profileAt
		err := rows.Scan(&r.Name, &r.InstanceId, &r.SlotId, &r.Endpoint, enumAt[v1.ApiFlavor]{&r.Api}, enumAt[v1.RouteState]{&r.State}, &r.Model, &r.Requests, at{&r.UpdatedAt}, &r.Served, &profile)
		r.Profile = profile.p
		return r, err
	})
}

// Stores a profile as its system message mode, empty when it leaves everything to the instance
func profileCol(p *v1.Profile) string {
	if p.GetSystemMessages() == v1.SystemMessages_SYSTEM_MESSAGES_UNSPECIFIED {
		return ""
	}
	return enumCol(p.GetSystemMessages())
}

// Scans a system message mode into a profile, none when the column is empty, as it was written
type profileAt struct{ p *v1.Profile }

func (a *profileAt) Scan(v any) error {
	a.p = nil
	s, _ := v.(string)
	if mode := enumOf[v1.SystemMessages](s); mode != v1.SystemMessages_SYSTEM_MESSAGES_UNSPECIFIED {
		a.p = &v1.Profile{SystemMessages: mode}
	}
	return nil
}

// Removes a route by name
func (d *DB) DeleteRoute(ctx context.Context, name string) error {
	_, err := d.del(ctx, "routes", "name", name)
	return err
}
