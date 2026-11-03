package db

import (
	"context"
	"database/sql"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// Inserts or replaces a pool, its spec as protobuf JSON
func (d *DB) PutPool(ctx context.Context, p *v1.Pool) error {
	spec, err := protojson.Marshal(p.GetSpec())
	if err != nil {
		return err
	}
	_, err = d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO pools (id, owner_kind, owner_id, generation, spec, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.GetId(), p.GetOwnerKind(), p.GetOwnerId(), int64(p.GetMeta().GetGeneration()), string(spec), stamp(createdOf(p.GetMeta())), stamp(updatedOf(p.GetMeta())))
	return err
}

// Lists every stored pool by id
func (d *DB) ListPools(ctx context.Context) ([]*v1.Pool, error) {
	return list(ctx, d, `SELECT id, owner_kind, owner_id, generation, spec, created_at, updated_at FROM pools ORDER BY id`, func(rows *sql.Rows) (*v1.Pool, error) {
		p := &v1.Pool{Meta: &v1.ObjectMeta{}, Spec: &v1.PoolSpec{}}
		var spec string
		var generation int64
		if err := rows.Scan(&p.Id, &p.OwnerKind, &p.OwnerId, &generation, &spec, at{&p.Meta.CreatedAt}, at{&p.Meta.UpdatedAt}); err != nil {
			return nil, err
		}
		p.Meta.Generation = uint64(generation)
		return p, unmarshalJSON(spec, p.Spec)
	})
}

// Removes a pool by id, true when one was stored
func (d *DB) DeletePool(ctx context.Context, id string) (bool, error) {
	return d.del(ctx, "pools", "id", id)
}

// Inserts or replaces an objective, keeping who holds it
func (d *DB) PutObjective(ctx context.Context, o *v1.Objective) error {
	status, err := statusCol(o.GetStatus())
	if err != nil {
		return err
	}
	_, err = d.sql.ExecContext(ctx, `INSERT INTO objectives (name, priority, ttft_ms, tpot_ms, generation, created_at, updated_at, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET priority = excluded.priority, ttft_ms = excluded.ttft_ms, tpot_ms = excluded.tpot_ms, generation = excluded.generation, created_at = excluded.created_at, updated_at = excluded.updated_at, status = excluded.status`,
		o.GetName(), o.GetPriority(), o.GetTtftMs(), o.GetTpotMs(), int64(o.GetMeta().GetGeneration()), stamp(createdOf(o.GetMeta())), stamp(updatedOf(o.GetMeta())), status)
	return err
}

// Sets who holds an objective, as the pool manager encodes them
func (d *DB) SetObjectiveOwners(ctx context.Context, name, owners string) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE objectives SET owners = ? WHERE name = ?`, owners, name)
	return err
}

// Who holds each objective by name, as the pool manager encoded them
func (d *DB) ObjectiveOwners(ctx context.Context) (map[string]string, error) {
	return d.stringMap(ctx, `SELECT name, owners FROM objectives`)
}

// Lists every objective by name
func (d *DB) ListObjectives(ctx context.Context) ([]*v1.Objective, error) {
	return list(ctx, d, `SELECT name, priority, ttft_ms, tpot_ms, generation, created_at, updated_at, status FROM objectives ORDER BY name`, func(rows *sql.Rows) (*v1.Objective, error) {
		o := &v1.Objective{Meta: &v1.ObjectMeta{}}
		var generation int64
		var status string
		if err := rows.Scan(&o.Name, &o.Priority, &o.TtftMs, &o.TpotMs, &generation, at{&o.Meta.CreatedAt}, at{&o.Meta.UpdatedAt}, &status); err != nil {
			return nil, err
		}
		o.Meta.Generation = uint64(generation)
		if status != "" {
			o.Status = &v1.ObjectiveStatus{}
			if err := unmarshalJSON(status, o.Status); err != nil {
				return nil, err
			}
		}
		return o, nil
	})
}

// Removes an objective by name, true when one was stored
func (d *DB) DeleteObjective(ctx context.Context, name string) (bool, error) {
	return d.del(ctx, "objectives", "name", name)
}

// Inserts or replaces a scheduler configuration with the YAML its spec carries as written
func (d *DB) PutSchedulerConfig(ctx context.Context, c *v1.SchedulerConfig) error {
	status, err := statusCol(c.GetStatus())
	if err != nil {
		return err
	}
	_, err = d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO scheduler_configs (name, yaml, generation, created_at, updated_at, status) VALUES (?, ?, ?, ?, ?, ?)`,
		c.GetName(), c.GetSpec().GetYaml(), int64(c.GetMeta().GetGeneration()), stamp(createdOf(c.GetMeta())), stamp(updatedOf(c.GetMeta())), status)
	return err
}

// Lists every stored scheduler configuration by name
func (d *DB) ListSchedulerConfigs(ctx context.Context) ([]*v1.SchedulerConfig, error) {
	return list(ctx, d, `SELECT name, yaml, generation, created_at, updated_at, status FROM scheduler_configs ORDER BY name`, func(rows *sql.Rows) (*v1.SchedulerConfig, error) {
		c := &v1.SchedulerConfig{Meta: &v1.ObjectMeta{}, Spec: &v1.SchedulerConfigSpec{}}
		var generation int64
		var status string
		if err := rows.Scan(&c.Name, &c.Spec.Yaml, &generation, at{&c.Meta.CreatedAt}, at{&c.Meta.UpdatedAt}, &status); err != nil {
			return nil, err
		}
		c.Meta.Generation = uint64(generation)
		if status != "" {
			c.Status = &v1.SchedulerConfigStatus{}
			if err := unmarshalJSON(status, c.Status); err != nil {
				return nil, err
			}
		}
		return c, nil
	})
}

// Removes a scheduler configuration by name, true when one was stored
func (d *DB) DeleteSchedulerConfig(ctx context.Context, name string) (bool, error) {
	return d.del(ctx, "scheduler_configs", "name", name)
}

// The creation time a resource carries, now when it carries none
func createdOf(m *v1.ObjectMeta) time.Time {
	if m.GetCreatedAt() == nil {
		return time.Now()
	}
	return m.GetCreatedAt().AsTime()
}

// The update time a resource carries, now when it carries none
func updatedOf(m *v1.ObjectMeta) time.Time {
	if m.GetUpdatedAt() == nil {
		return time.Now()
	}
	return m.GetUpdatedAt().AsTime()
}

// Inserts or replaces the user override of an owner route, its spec carrying policy, profile and objective
func (d *DB) PutRouteOverride(ctx context.Context, r *v1.Route) error {
	spec, err := protojson.Marshal(r.GetSpec())
	if err != nil {
		return err
	}
	_, err = d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO route_overrides (name, spec, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		r.GetName(), string(spec), stamp(createdOf(r.GetMeta())), stamp(updatedOf(r.GetMeta())))
	return err
}

// Lists every route override by route name
func (d *DB) ListRouteOverrides(ctx context.Context) ([]*v1.Route, error) {
	return list(ctx, d, `SELECT name, spec, created_at, updated_at FROM route_overrides ORDER BY name`, func(rows *sql.Rows) (*v1.Route, error) {
		r := &v1.Route{Meta: &v1.ObjectMeta{}, Spec: &v1.RouteSpec{}}
		var spec string
		if err := rows.Scan(&r.Name, &spec, at{&r.Meta.CreatedAt}, at{&r.Meta.UpdatedAt}); err != nil {
			return nil, err
		}
		return r, unmarshalJSON(spec, r.Spec)
	})
}

// Removes a route override by route name, true when one was stored
func (d *DB) DeleteRouteOverride(ctx context.Context, name string) (bool, error) {
	return d.del(ctx, "route_overrides", "name", name)
}
