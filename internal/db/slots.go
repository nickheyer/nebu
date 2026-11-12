package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Inserts or replaces a slot with devices, params, and request
func (d *DB) PutSlot(ctx context.Context, s *v1.Slot) error {
	return d.tx(ctx, func(exec execFn) error {
		id := s.GetId()
		p := s.GetPolicy()
		conditions, err := conditionsCol(s.GetConditions())
		if err != nil {
			return err
		}
		caps, err := poolBytesCol(s.GetPoolBytes())
		if err != nil {
			return err
		}
		if err := exec(`INSERT OR REPLACE INTO slots (id, name, position, placement, runtime_id, memory_bytes, instance_id, state, error, task_id, created_at, updated_at, max_in_flight, requests_per_second, burst, request_timeout_ms, upstream_timeout_ms, formation_id, profile, args, description, desired_running, conditions, meta, observed_generation, pool_bytes, install_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, s.GetName(), s.GetPosition(), enumCol(s.GetPlacement()), s.GetRuntimeId(), int64(s.GetMemoryBytes()), s.GetInstanceId(), enumCol(s.GetState()), s.GetError(), s.GetTaskId(), stamp(s.GetCreatedAt().AsTime()), stamp(s.GetUpdatedAt().AsTime()),
			p.GetMaxInFlight(), p.GetRequestsPerSecond(), p.GetBurst(), p.GetRequestTimeoutMs(), p.GetUpstreamTimeoutMs(), s.GetFormationId(), profileCol(s.GetProfile()), s.GetArgs(),
			s.GetDescription(), boolCol(s.GetDesiredRunning()), conditions, metaCol(s.GetMeta()), int64(s.GetObservedGeneration()), caps, s.GetInstallId()); err != nil {
			return err
		}
		if err := clearChildren(exec, "slot_id", id, "slot_devices", "slot_aliases", "slot_params", "slot_requests", "slot_request_params"); err != nil {
			return err
		}
		for i, dev := range s.GetDeviceIds() {
			if err := exec(`INSERT INTO slot_devices (slot_id, position, device_id) VALUES (?, ?, ?)`, id, i, dev); err != nil {
				return err
			}
		}
		for i, a := range s.GetAliases() {
			if err := exec(`INSERT INTO slot_aliases (slot_id, position, name, inherit_slot_id) VALUES (?, ?, ?, ?)`, id, i, a.GetName(), a.GetInheritSlotId()); err != nil {
				return err
			}
		}
		if err := putMap(exec, `INSERT INTO slot_params (slot_id, name, value) VALUES (?, ?, ?)`, id, s.GetParams()); err != nil {
			return err
		}
		if req := s.GetRequest(); req != nil {
			if err := exec(`INSERT INTO slot_requests (slot_id, source_id, repo, weight_group, runtime_id, install_id, name, span, shape, profile, args, route_policy, route_profile, idle_unload_seconds, snapshot) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, req.GetSourceId(), req.GetRepo(), req.GetGroup(), req.GetRuntimeId(), req.GetInstallId(), req.GetName(), strings.Join(req.GetSpan(), ","), enumCol(req.GetShape()), enumCol(req.GetProfile()), req.GetArgs(), messageCol(req.GetPolicy()), profileCol(req.GetRouteProfile()), req.GetIdleUnloadSeconds(), boolCol(req.GetSnapshot())); err != nil {
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
	out, err := list(ctx, d, `SELECT id, name, position, placement, runtime_id, memory_bytes, instance_id, state, error, task_id, created_at, updated_at, max_in_flight, requests_per_second, burst, request_timeout_ms, upstream_timeout_ms, formation_id, profile, args, description, desired_running, conditions, meta, observed_generation, pool_bytes, install_id FROM slots ORDER BY position, name`, func(rows *sql.Rows) (*v1.Slot, error) {
		s := &v1.Slot{}
		p := &v1.Policy{}
		var profile profileAt
		var conditions, meta string
		var observed int64
		if err := rows.Scan(&s.Id, &s.Name, &s.Position, enumAt[v1.Placement]{&s.Placement}, &s.RuntimeId, &s.MemoryBytes, &s.InstanceId, enumAt[v1.SlotState]{&s.State}, &s.Error, &s.TaskId, at{&s.CreatedAt}, at{&s.UpdatedAt}, &p.MaxInFlight, &p.RequestsPerSecond, &p.Burst, &p.RequestTimeoutMs, &p.UpstreamTimeoutMs, &s.FormationId, &profile, &s.Args,
			&s.Description, (*flag)(&s.DesiredRunning), &conditions, &meta, &observed, poolBytesAt{&s.PoolBytes}, &s.InstallId); err != nil {
			return nil, err
		}
		// A slot that inherits everything carries no policy, as it was written
		if p.GetMaxInFlight()+p.GetBurst()+p.GetRequestTimeoutMs()+p.GetUpstreamTimeoutMs() > 0 || p.GetRequestsPerSecond() > 0 {
			s.Policy = p
		}
		s.Profile = profile.p
		s.ObservedGeneration = uint64(observed)
		var err error
		if s.Conditions, err = conditionsOf(conditions); err != nil {
			return nil, err
		}
		if meta != "" {
			s.Meta = &v1.ObjectMeta{}
			if err := unmarshalJSON(meta, s.Meta); err != nil {
				return nil, err
			}
		}
		return s, nil
	})
	if err != nil {
		return nil, err
	}
	for _, s := range out {
		if s.DeviceIds, err = d.strings(ctx, `SELECT device_id FROM slot_devices WHERE slot_id = ? ORDER BY position`, s.GetId()); err != nil {
			return nil, err
		}
		if s.Aliases, err = list(ctx, d, `SELECT name, inherit_slot_id FROM slot_aliases WHERE slot_id = ? ORDER BY position`, func(rows *sql.Rows) (*v1.SlotAlias, error) {
			a := &v1.SlotAlias{}
			return a, rows.Scan(&a.Name, &a.InheritSlotId)
		}, s.GetId()); err != nil {
			return nil, err
		}
		if s.Params, err = d.stringMap(ctx, `SELECT name, value FROM slot_params WHERE slot_id = ? ORDER BY name`, s.GetId()); err != nil {
			return nil, err
		}
		requests, err := list(ctx, d, `SELECT source_id, repo, weight_group, runtime_id, install_id, name, span, shape, profile, args, route_policy, route_profile, idle_unload_seconds, snapshot FROM slot_requests WHERE slot_id = ?`, func(rows *sql.Rows) (*v1.RunRequest, error) {
			req := &v1.RunRequest{SlotId: s.GetId()}
			var span spanAt
			var policy policyAt
			var profile profileAt
			err := rows.Scan(&req.SourceId, &req.Repo, &req.Group, &req.RuntimeId, &req.InstallId, &req.Name, &span, enumAt[v1.Shape]{&req.Shape}, enumAt[v1.PlanProfile]{&req.Profile}, &req.Args, &policy, &profile, &req.IdleUnloadSeconds, (*flag)(&req.Snapshot))
			req.Span, req.Policy, req.RouteProfile = span.ids, policy.p, profile.p
			return req, err
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

// Inserts or replaces a route, its spec and status as protobuf JSON
func (d *DB) PutRoute(ctx context.Context, r *v1.Route) error {
	spec, err := protojson.Marshal(r.GetSpec())
	if err != nil {
		return err
	}
	status, err := protojson.Marshal(r.GetStatus())
	if err != nil {
		return err
	}
	created := r.GetMeta().GetCreatedAt().AsTime()
	if r.GetMeta().GetCreatedAt() == nil {
		created = time.Now()
	}
	_, err = d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO routes (name, owner_kind, owner_id, node_id, generation, spec, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.GetName(), r.GetOwnerKind(), r.GetOwnerId(), r.GetNodeId(), int64(r.GetMeta().GetGeneration()), string(spec), string(status), stamp(created), stamp(time.Now()))
	return err
}

// Lists every route by name
func (d *DB) ListRoutes(ctx context.Context) ([]*v1.Route, error) {
	return list(ctx, d, `SELECT name, owner_kind, owner_id, node_id, generation, spec, status, created_at, updated_at FROM routes ORDER BY name`, func(rows *sql.Rows) (*v1.Route, error) {
		r := &v1.Route{Meta: &v1.ObjectMeta{}, Spec: &v1.RouteSpec{}, Status: &v1.RouteStatus{}}
		var spec, status string
		var generation int64
		if err := rows.Scan(&r.Name, &r.OwnerKind, &r.OwnerId, &r.NodeId, &generation, &spec, &status, at{&r.Meta.CreatedAt}, at{&r.Meta.UpdatedAt}); err != nil {
			return nil, err
		}
		r.Meta.Generation = uint64(generation)
		if err := unmarshalJSON(spec, r.Spec); err != nil {
			return nil, err
		}
		if err := unmarshalJSON(status, r.Status); err != nil {
			return nil, err
		}
		return r, nil
	})
}

// Stores a profile as JSON, empty when every field inherits
func profileCol(p *v1.Profile) string {
	if p == nil || proto.Size(p) == 0 {
		return ""
	}
	data, _ := protojson.Marshal(p)
	return string(data)
}

// Scans a profile from its JSON, none when the column is empty, as it was written
type profileAt struct{ p *v1.Profile }

func (a *profileAt) Scan(v any) error {
	s, _ := v.(string)
	if s == "" {
		a.p = nil
		return nil
	}
	a.p = &v1.Profile{}
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal([]byte(s), a.p)
}

// Stores pool caps as a JSON object of pool id to bytes, empty text when there are none
func poolBytesCol(caps map[string]uint64) (string, error) {
	if len(caps) == 0 {
		return "", nil
	}
	data, err := json.Marshal(caps)
	return string(data), err
}

// Scans pool caps from their JSON object, none when the column is empty
type poolBytesAt struct{ p *map[string]uint64 }

func (a poolBytesAt) Scan(v any) error {
	s, _ := v.(string)
	*a.p = nil
	if s == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), a.p)
}

// Stores a message as protobuf JSON, empty text for an unset or empty message
func messageCol(m proto.Message) string {
	if m == nil || !m.ProtoReflect().IsValid() || proto.Size(m) == 0 {
		return ""
	}
	data, _ := protojson.Marshal(m)
	return string(data)
}

// Scans a policy from its JSON, none when the column is empty
type policyAt struct{ p *v1.Policy }

func (a *policyAt) Scan(v any) error {
	s, _ := v.(string)
	if s == "" {
		a.p = nil
		return nil
	}
	a.p = &v1.Policy{}
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal([]byte(s), a.p)
}

// Stores generation, labels and annotations as JSON, the timestamps kept in their own columns
func metaCol(m *v1.ObjectMeta) string {
	if m == nil {
		return ""
	}
	return messageCol(&v1.ObjectMeta{Generation: m.GetGeneration(), Labels: m.GetLabels(), Annotations: m.GetAnnotations()})
}

// Stores conditions as JSON inside an instance wrapper, empty text when there are none
func conditionsCol(list []*v1.Condition) (string, error) {
	if len(list) == 0 {
		return "", nil
	}
	data, err := protojson.Marshal(&v1.Instance{Conditions: list})
	return string(data), err
}

// Reads conditions stored by conditionsCol, none for empty text
func conditionsOf(s string) ([]*v1.Condition, error) {
	wrapped := &v1.Instance{}
	if err := unmarshalJSON(s, wrapped); err != nil {
		return nil, err
	}
	return wrapped.GetConditions(), nil
}

// Reads a protobuf JSON column, an empty column leaving the message empty
func unmarshalJSON(s string, m proto.Message) error {
	if s == "" {
		return nil
	}
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal([]byte(s), m)
}

// Removes a route by name
func (d *DB) DeleteRoute(ctx context.Context, name string) error {
	_, err := d.del(ctx, "routes", "name", name)
	return err
}
