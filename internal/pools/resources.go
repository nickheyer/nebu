package pools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/nickheyer/nebu/pkg/conditions"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/scheduling/config"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Bounds an objective priority is held to
const (
	minPriority = -1000
	maxPriority = 1000
)

// Built in scheduler configurations by name
var builtinConfigs = map[string]string{
	SchedulerDefault:   config.DefaultConfig,
	SchedulerNoMetrics: config.DefaultNoMetricsConfig,
	SchedulerPD:        config.DefaultPDConfig,
	SchedulerReplicas:  config.DefaultReplicasConfig,
}

// Creates or replaces an objective the user holds, bumping its generation when its targets change
func (m *Manager) PutObjective(ctx context.Context, o *v1.Objective) (*v1.Objective, error) {
	if err := validObjective(o); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out, err := m.putObjectiveLocked(ctx, o)
	if err != nil {
		return nil, err
	}
	return out, m.holdObjectiveLocked(ctx, o.GetName(), OwnerRef{Kind: OwnerUser})
}

// Holds an objective for an owner, a user's objective keeping its targets and any other taking the latest
func (m *Manager) PutObjectiveFor(ctx context.Context, owner OwnerRef, o *v1.Objective) (*v1.Objective, error) {
	if err := validOwner(owner); err != nil {
		return nil, err
	}
	if err := validObjective(o); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.st.objectives[o.GetName()]
	if out == nil || !m.st.objectiveOwners[o.GetName()][OwnerRef{Kind: OwnerUser}] {
		var err error
		if out, err = m.putObjectiveLocked(ctx, o); err != nil {
			return nil, err
		}
	}
	if err := m.holdObjectiveLocked(ctx, o.GetName(), owner); err != nil {
		return nil, err
	}
	m.publishViewLocked()
	return proto.Clone(out).(*v1.Objective), nil
}

// Refuses an objective without a name or with a priority out of bounds
func validObjective(o *v1.Objective) error {
	switch {
	case o.GetName() == "":
		return fmt.Errorf("%w: objective without a name", ErrInvalid)
	case o.GetPriority() < minPriority || o.GetPriority() > maxPriority:
		return fmt.Errorf("%w: objective %s priority %d is outside %d to %d", ErrInvalid, o.GetName(), o.GetPriority(), minPriority, maxPriority)
	}
	return nil
}

// Adds an owner to those holding an objective and writes the set
func (m *Manager) holdObjectiveLocked(ctx context.Context, name string, owner OwnerRef) error {
	held := m.st.objectiveOwners[name]
	if held[owner] {
		return nil
	}
	next := maps.Clone(held)
	if next == nil {
		next = map[OwnerRef]bool{}
	}
	next[owner] = true
	if err := m.db.SetObjectiveOwners(ctx, name, encodeOwners(next)); err != nil {
		return err
	}
	m.st.objectiveOwners[name] = next
	return nil
}

// Lets go of one objective the owner held, deleting it once nobody holds it and no route names it
func (m *Manager) DropObjective(ctx context.Context, owner OwnerRef, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropObjectiveLocked(ctx, owner, name)
	m.publishViewLocked()
}

// Lets go of every objective the owner held
func (m *Manager) DropObjectives(ctx context.Context, owner OwnerRef) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, name := range slices.Sorted(maps.Keys(m.st.objectiveOwners)) {
		m.dropObjectiveLocked(ctx, owner, name)
	}
	m.publishViewLocked()
}

// Removes the owner from an objective, a route still naming the last held one handing it to the user
func (m *Manager) dropObjectiveLocked(ctx context.Context, owner OwnerRef, name string) {
	held := m.st.objectiveOwners[name]
	if !held[owner] {
		return
	}
	next := maps.Clone(held)
	delete(next, owner)
	if len(next) == 0 {
		if m.objectiveRouteLocked(name) == "" {
			if _, err := m.deleteObjectiveLocked(ctx, name); err != nil {
				m.log.Warn("objective not deleted", "objective", name, "err", err)
			}
			return
		}
		next[OwnerRef{Kind: OwnerUser}] = true
	}
	if err := m.db.SetObjectiveOwners(ctx, name, encodeOwners(next)); err != nil {
		m.log.Warn("objective owners not written", "objective", name, "err", err)
		return
	}
	m.st.objectiveOwners[name] = next
}

// The first route naming the objective in its spec or under a user override, empty when none does
func (m *Manager) objectiveRouteLocked(name string) string {
	for _, rn := range slices.Sorted(maps.Keys(m.st.routes)) {
		rs := m.st.routes[rn]
		if rs.route.GetSpec().GetObjective() == name || rs.base.GetObjective() == name {
			return rn
		}
	}
	return ""
}

// The holders of an objective as stored, kind and id pairs in JSON
func encodeOwners(held map[OwnerRef]bool) string {
	list := make([][2]string, 0, len(held))
	for o := range held {
		list = append(list, [2]string{o.Kind, o.ID})
	}
	slices.SortFunc(list, func(a, b [2]string) int { return cmp.Or(strings.Compare(a[0], b[0]), strings.Compare(a[1], b[1])) })
	raw, _ := json.Marshal(list)
	return string(raw)
}

// The holders of an objective read back, the user alone for one stored before holders were kept
func decodeOwners(s string) (map[OwnerRef]bool, error) {
	out := map[OwnerRef]bool{}
	if s == "" {
		out[OwnerRef{Kind: OwnerUser}] = true
		return out, nil
	}
	var list [][2]string
	if err := json.Unmarshal([]byte(s), &list); err != nil {
		return nil, err
	}
	for _, o := range list {
		out[OwnerRef{Kind: o[0], ID: o[1]}] = true
	}
	return out, nil
}

// Writes an objective's targets, bumping its generation when they change
func (m *Manager) putObjectiveLocked(ctx context.Context, o *v1.Objective) (*v1.Objective, error) {
	cur := m.st.objectives[o.GetName()]
	if cur != nil && cur.GetPriority() == o.GetPriority() && cur.GetTtftMs() == o.GetTtftMs() && cur.GetTpotMs() == o.GetTpotMs() {
		return proto.Clone(cur).(*v1.Objective), nil
	}
	now := timestamppb.Now()
	meta := &v1.ObjectMeta{CreatedAt: now}
	if cur != nil {
		meta = proto.Clone(cur.GetMeta()).(*v1.ObjectMeta)
	}
	meta.Generation++
	meta.UpdatedAt = now
	next := &v1.Objective{Name: o.GetName(), Priority: o.GetPriority(), TtftMs: o.GetTtftMs(), TpotMs: o.GetTpotMs(), Meta: meta, Status: objectiveStatus(cur, meta.GetGeneration())}
	if err := m.db.PutObjective(ctx, next); err != nil {
		return nil, err
	}
	m.st.objectives[next.GetName()] = next
	action := v1.EventAction_EVENT_ACTION_UPDATED
	if cur == nil {
		action = v1.EventAction_EVENT_ACTION_CREATED
	}
	m.emitLocked(v1.EventKind_EVENT_KIND_OBJECTIVE, action, next.GetName(), next)
	m.publishViewLocked()
	return proto.Clone(next).(*v1.Objective), nil
}

// The status of an objective at a generation, Accepted and Ready, transition times kept from the status before
func objectiveStatus(prev *v1.Objective, gen uint64) *v1.ObjectiveStatus {
	conds := conditionsOf(prev.GetStatus().GetConditions())
	conditions.Set(&conds, conditions.Condition{Type: ConditionAccepted, Status: conditions.True, Reason: ReasonAccepted, Message: "the objective is valid", ObservedGeneration: int64(gen)})
	conditions.Set(&conds, conditions.Condition{Type: ConditionReady, Status: conditions.True, Reason: ReasonReady, Message: "routes and requests may name the objective", ObservedGeneration: int64(gen)})
	return &v1.ObjectiveStatus{Conditions: protoConditions(conds), ObservedGeneration: gen}
}

// Removes an objective no route names, whoever holds it
func (m *Manager) DeleteObjective(ctx context.Context, name string) (*v1.Objective, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deleteObjectiveLocked(ctx, name)
}

// Removes an objective no route names with its holders
func (m *Manager) deleteObjectiveLocked(ctx context.Context, name string) (*v1.Objective, error) {
	cur := m.st.objectives[name]
	if cur == nil {
		return nil, fmt.Errorf("%w: objective %s", ErrNotFound, name)
	}
	if rn := m.objectiveRouteLocked(name); rn != "" {
		return nil, fmt.Errorf("%w: route %s names objective %s", ErrInUse, rn, name)
	}
	if _, err := m.db.DeleteObjective(ctx, name); err != nil {
		return nil, err
	}
	delete(m.st.objectives, name)
	delete(m.st.objectiveOwners, name)
	m.emitLocked(v1.EventKind_EVENT_KIND_OBJECTIVE, v1.EventAction_EVENT_ACTION_DELETED, name, cur)
	m.publishViewLocked()
	return proto.Clone(cur).(*v1.Objective), nil
}

// Every objective by name
func (m *Manager) Objectives() []*v1.Objective {
	v := m.view.Load()
	out := make([]*v1.Objective, 0, len(v.objectives))
	for _, name := range slices.Sorted(maps.Keys(v.objectives)) {
		out = append(out, proto.Clone(v.objectives[name]).(*v1.Objective))
	}
	return out
}

// The objective of a name, read without the mutation lock
func (m *Manager) Objective(name string) (*v1.Objective, bool) {
	o, ok := m.view.Load().objectives[name]
	if !ok {
		return nil, false
	}
	return proto.Clone(o).(*v1.Objective), true
}

// Loads YAML against the gateway's scheduler dependencies when set, against fresh ones otherwise
func (m *Manager) Validate(yaml string) error {
	m.mu.Lock()
	deps := m.deps
	m.mu.Unlock()
	return validateWith(deps, yaml)
}

// Refuses YAML exactly as a scheduler config put would, storing nothing, for validate only requests
func (m *Manager) ValidateSchedulerConfig(yaml string) error {
	m.mu.Lock()
	deps := m.deps
	m.mu.Unlock()
	return checkConfig(deps, yaml)
}

// Refuses empty YAML or YAML that does not load, wrapping the refusal as invalid
func checkConfig(deps SchedulerDeps, yaml string) error {
	if yaml == "" {
		return fmt.Errorf("%w: scheduler config has no YAML", ErrInvalid)
	}
	if err := validateWith(deps, yaml); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

func validateWith(deps SchedulerDeps, yaml string) error {
	if deps != nil {
		_, _, err := config.Load([]byte(yaml), deps.SchedulerDeps())
		return err
	}
	return config.Validate([]byte(yaml))
}

// Sets the dependencies scheduler configurations load against and checks every held configuration with them
func (m *Manager) SetSchedulerDeps(ctx context.Context, deps SchedulerDeps) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deps = deps
	for _, name := range slices.Sorted(maps.Keys(m.st.configs)) {
		cs := m.st.configs[name]
		cs.err = validateWith(deps, cs.cfg.GetSpec().GetYaml())
		if !m.configStatusLocked(cs) {
			continue
		}
		if cs.stored {
			if err := m.db.PutSchedulerConfig(ctx, cs.record); err != nil {
				m.log.Error("scheduler config status not stored", "config", name, "err", err)
			}
		}
		m.emitLocked(v1.EventKind_EVENT_KIND_SCHEDULER_CONFIG, v1.EventAction_EVENT_ACTION_UPDATED, name, cs.record)
	}
	m.refreshPoolsLocked()
	m.refreshRoutesLocked(ctx)
	m.publishViewLocked()
}

// Creates or replaces a scheduler configuration, refusing YAML that does not load
func (m *Manager) PutSchedulerConfig(ctx context.Context, c *v1.SchedulerConfig) (*v1.SchedulerConfig, error) {
	if c.GetName() == "" {
		return nil, fmt.Errorf("%w: scheduler config without a name", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := checkConfig(m.deps, c.GetSpec().GetYaml()); err != nil {
		return nil, fmt.Errorf("scheduler config %s: %w", c.GetName(), err)
	}
	cur := m.st.configs[c.GetName()]
	if cur != nil && cur.cfg.GetSpec().GetYaml() == c.GetSpec().GetYaml() && cur.err == nil {
		return proto.Clone(cur.record).(*v1.SchedulerConfig), nil
	}
	now := timestamppb.Now()
	meta := &v1.ObjectMeta{CreatedAt: now}
	if cur != nil {
		meta = proto.Clone(cur.cfg.GetMeta()).(*v1.ObjectMeta)
	}
	meta.Generation++
	meta.UpdatedAt = now
	cfg := &v1.SchedulerConfig{Name: c.GetName(), Spec: &v1.SchedulerConfigSpec{Yaml: c.GetSpec().GetYaml()}, Meta: meta}
	cs := &configState{cfg: cfg, builtin: builtinConfigs[cfg.GetName()] != "", stored: true}
	if cur != nil {
		cs.conds = cur.conds
	}
	m.configStatusLocked(cs)
	if err := m.db.PutSchedulerConfig(ctx, cs.record); err != nil {
		return nil, err
	}
	m.st.configs[cfg.GetName()] = cs
	action := v1.EventAction_EVENT_ACTION_UPDATED
	if cur == nil {
		action = v1.EventAction_EVENT_ACTION_CREATED
	}
	m.emitLocked(v1.EventKind_EVENT_KIND_SCHEDULER_CONFIG, action, cfg.GetName(), cs.record)
	m.refreshPoolsLocked()
	m.refreshRoutesLocked(ctx)
	m.publishViewLocked()
	return proto.Clone(cs.record).(*v1.SchedulerConfig), nil
}

// Removes a scheduler configuration that is not built in and that no pool runs
func (m *Manager) DeleteSchedulerConfig(ctx context.Context, name string) (*v1.SchedulerConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cs := m.st.configs[name]
	switch {
	case cs == nil:
		return nil, fmt.Errorf("%w: scheduler config %s", ErrNotFound, name)
	case cs.builtin:
		return nil, fmt.Errorf("%w: scheduler config %s is built in", ErrManaged, name)
	}
	for _, id := range slices.Sorted(maps.Keys(m.st.pools)) {
		if m.st.pools[id].spec.GetSchedulerConfig() == name {
			return nil, fmt.Errorf("%w: pool %s runs scheduler config %s", ErrInUse, id, name)
		}
	}
	if _, err := m.db.DeleteSchedulerConfig(ctx, name); err != nil {
		return nil, err
	}
	delete(m.st.configs, name)
	m.emitLocked(v1.EventKind_EVENT_KIND_SCHEDULER_CONFIG, v1.EventAction_EVENT_ACTION_DELETED, name, cs.record)
	m.publishViewLocked()
	return proto.Clone(cs.record).(*v1.SchedulerConfig), nil
}

// Every scheduler configuration by name
func (m *Manager) SchedulerConfigs() []*v1.SchedulerConfig {
	v := m.view.Load()
	out := make([]*v1.SchedulerConfig, 0, len(v.configList))
	for _, name := range v.configList {
		out = append(out, proto.Clone(v.configs[name]).(*v1.SchedulerConfig))
	}
	return out
}

// The scheduler configuration of a name with its YAML
func (m *Manager) SchedulerConfig(name string) (*v1.SchedulerConfig, bool) {
	c, ok := m.view.Load().configs[name]
	if !ok {
		return nil, false
	}
	return proto.Clone(c).(*v1.SchedulerConfig), true
}

// Sets the Accepted condition from the last load and rebuilds the record, true when the record changed
func (m *Manager) configStatusLocked(cs *configState) bool {
	c := conditions.Condition{Type: ConditionAccepted, Status: conditions.True, Reason: ReasonAccepted, Message: "the configuration loads", ObservedGeneration: int64(cs.cfg.GetMeta().GetGeneration())}
	if cs.err != nil {
		c.Status, c.Reason, c.Message = conditions.False, ReasonInvalid, cs.err.Error()
	}
	if !conditions.Set(&cs.conds, c) && cs.record != nil {
		return false
	}
	record := proto.Clone(cs.cfg).(*v1.SchedulerConfig)
	record.Status = &v1.SchedulerConfigStatus{Conditions: protoConditions(cs.conds), ObservedGeneration: cs.cfg.GetMeta().GetGeneration()}
	cs.record = record
	return true
}

// Recomputes every pool's status, announcing those that changed
func (m *Manager) refreshPoolsLocked() {
	for _, id := range slices.Sorted(maps.Keys(m.st.pools)) {
		if m.refreshPoolLocked(m.st.pools[id]) {
			m.coalesceLocked(eventKey{kind: v1.EventKind_EVENT_KIND_POOL, id: string(id)})
		}
	}
}

// Restores scheduler configurations, objectives, user pools and every persisted route
func (m *Manager) loadLocked(ctx context.Context) error {
	now := timestamppb.Now()
	for name, yaml := range builtinConfigs {
		m.st.configs[name] = &configState{cfg: &v1.SchedulerConfig{Name: name, Spec: &v1.SchedulerConfigSpec{Yaml: yaml}, Meta: &v1.ObjectMeta{Generation: 1, CreatedAt: now, UpdatedAt: now}}, builtin: true}
	}
	configs, err := m.db.ListSchedulerConfigs(ctx)
	if err != nil {
		return fmt.Errorf("load scheduler configs: %w", err)
	}
	for _, c := range configs {
		m.st.configs[c.GetName()] = &configState{cfg: c, builtin: builtinConfigs[c.GetName()] != "", stored: true, conds: conditionsOf(c.GetStatus().GetConditions())}
	}
	for _, name := range slices.Sorted(maps.Keys(m.st.configs)) {
		cs := m.st.configs[name]
		cs.err = config.Validate([]byte(cs.cfg.GetSpec().GetYaml()))
		if m.configStatusLocked(cs) && cs.stored {
			if err := m.db.PutSchedulerConfig(ctx, cs.record); err != nil {
				return fmt.Errorf("store scheduler config %s status: %w", name, err)
			}
		}
	}
	objectives, err := m.db.ListObjectives(ctx)
	if err != nil {
		return fmt.Errorf("load objectives: %w", err)
	}
	owners, err := m.db.ObjectiveOwners(ctx)
	if err != nil {
		return fmt.Errorf("load objective holders: %w", err)
	}
	for _, o := range objectives {
		held, err := decodeOwners(owners[o.GetName()])
		if err != nil {
			return fmt.Errorf("load objective %s holders: %w", o.GetName(), err)
		}
		if o.GetStatus() == nil {
			o.Status = objectiveStatus(nil, o.GetMeta().GetGeneration())
			if err := m.db.PutObjective(ctx, o); err != nil {
				return fmt.Errorf("store objective %s status: %w", o.GetName(), err)
			}
		}
		m.st.objectives[o.GetName()] = o
		m.st.objectiveOwners[o.GetName()] = held
	}
	pools, err := m.db.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("load pools: %w", err)
	}
	for _, p := range pools {
		addrs, err := validPoolSpec(p.GetId(), p.GetSpec())
		if err != nil {
			return fmt.Errorf("load pool %s: %w", p.GetId(), err)
		}
		ps := m.newPoolLocked(datalayer.PoolID(p.GetId()))
		ps.meta = p.GetMeta()
		ps.user, ps.userAddrs = p.GetSpec(), addrs
	}
	routes, err := m.db.ListRoutes(ctx)
	if err != nil {
		return fmt.Errorf("load routes: %w", err)
	}
	for _, r := range routes {
		m.restoreRouteLocked(r)
	}
	overrides, err := m.db.ListRouteOverrides(ctx)
	if err != nil {
		return fmt.Errorf("load route overrides: %w", err)
	}
	for _, o := range overrides {
		rs := m.st.routes[o.GetName()]
		if rs == nil || rs.owner == (OwnerRef{}) {
			if _, err := m.db.DeleteRouteOverride(ctx, o.GetName()); err != nil {
				return fmt.Errorf("delete override of missing owner route %s: %w", o.GetName(), err)
			}
			continue
		}
		rs.override = o
		rs.base = &v1.RouteSpec{}
	}
	for _, id := range slices.Sorted(maps.Keys(m.st.pools)) {
		ps := m.st.pools[id]
		stored := ps.meta
		m.commitLocked(ps, m.planLocked(ps))
		if ps.user != nil {
			ps.meta = proto.Clone(stored).(*v1.ObjectMeta)
			ps.meta.Generation = max(stored.GetGeneration(), 1)
		}
		m.refreshPoolLocked(ps)
	}
	m.refreshRoutesLocked(ctx)
	return nil
}

// Rebuilds a persisted route, holding a pool for an owner route until its owner publishes again
func (m *Manager) restoreRouteLocked(r *v1.Route) {
	rs := &routeState{route: r, conds: conditionsOf(r.GetStatus().GetConditions()), parents: parentsOf(r.GetStatus().GetParents()), shape: r.GetStatus().GetShape(), seats: r.GetStatus().GetSeats(), nodeID: r.GetNodeId()}
	gw := conditions.ParentRef{Kind: "Gateway", Name: m.nodeID}
	rs.parents = slices.DeleteFunc(rs.parents, func(p conditions.ParentStatus) bool {
		return p.ControllerName == GatewayController && p.ParentRef != gw
	})
	rs.shared = map[OwnerRef]sharedTarget{}
	if r.GetOwnerKind() == "" {
		rs.user = r.GetSpec()
	} else {
		rs.base = &v1.RouteSpec{Policy: r.GetSpec().GetPolicy(), Profile: r.GetSpec().GetProfile(), Objective: r.GetSpec().GetObjective(), Balance: r.GetSpec().GetBalance()}
		rs.own = &v1.RewriteRule{Match: r.GetName()}
		if rules := r.GetSpec().GetRules(); len(rules) > 0 {
			rs.own = rules[0]
		}
		rs.owner = OwnerRef{Kind: r.GetOwnerKind(), ID: r.GetOwnerId()}
		if m.st.owners[rs.owner] == nil {
			pool := datalayer.PoolID(firstTarget(r))
			os := &ownerState{ref: rs.owner, pool: pool, joined: map[datalayer.EndpointID]joinedEndpoint{}}
			m.st.owners[rs.owner] = os
			if pool != "" {
				ps := m.st.pools[pool]
				if ps == nil {
					ps = m.newPoolLocked(pool)
				}
				ps.owners = append(ps.owners, rs.owner)
			}
		}
	}
	m.st.routes[r.GetName()] = rs
}

// Parent entries read back from a stored status, keeping their transition times
func parentsOf(in []*v1.ParentStatus) []conditions.ParentStatus {
	out := make([]conditions.ParentStatus, 0, len(in))
	for _, p := range in {
		ref := p.GetParentRef()
		out = append(out, conditions.ParentStatus{
			ParentRef:      conditions.ParentRef{Group: ref.GetGroup(), Kind: ref.GetKind(), Name: ref.GetName(), Namespace: ref.GetNamespace()},
			ControllerName: p.GetControllerName(),
			Conditions:     conditionsOf(p.GetConditions()),
		})
	}
	return out
}

// The pool of a route's first target, empty when it has none
func firstTarget(r *v1.Route) string {
	for _, rule := range r.GetSpec().GetRules() {
		for _, t := range rule.GetTargets() {
			return t.GetPoolId()
		}
	}
	return ""
}
