package pools

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/conditions"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// What one pool plan would write, the spec, its capabilities and each owner's endpoints
type poolPlan struct {
	spec *v1.PoolSpec
	caps datalayer.MetricCapabilities
	want map[OwnerRef][]joinedEndpoint
}

// What a commit changed, the spec or endpoint membership against readiness alone
type change struct {
	spec      bool
	members   bool
	readiness bool
}

func (m *Manager) Publish(ctx context.Context, owner OwnerRef, mem Membership, endpoints []Published) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.publishLocked(ctx, owner, mem, endpoints); err != nil {
		return err
	}
	m.publishViewLocked()
	return nil
}

// Validates and applies one owner's membership and endpoints, moving it between pools when its model or runtime changed
func (m *Manager) publishLocked(ctx context.Context, owner OwnerRef, mem Membership, endpoints []Published) error {
	if err := validOwner(owner); err != nil {
		return err
	}
	if err := validMembership(&mem); err != nil {
		return err
	}
	id := PoolIDOf(mem.Model, mem.Runtime, mem.Accelerator)
	seen := map[datalayer.EndpointID]bool{}
	for i := range endpoints {
		p := &endpoints[i]
		if err := validPublished(p); err != nil {
			return err
		}
		eid := endpointID(p)
		if seen[eid] {
			return fmt.Errorf("%w: endpoint %s published twice", ErrInvalid, eid)
		}
		seen[eid] = true
		if holder, ok := m.st.endpoints[eid]; ok && holder != owner {
			return fmt.Errorf("%w: endpoint %s is published by %s %s", ErrConflict, eid, holder.Kind, holder.ID)
		}
	}
	if ps := m.st.pools[id]; ps != nil {
		if err := m.compatibleLocked(ps, owner, &mem); err != nil {
			return err
		}
	}
	os := m.st.owners[owner]
	if os == nil {
		os = &ownerState{ref: owner, joined: map[datalayer.EndpointID]joinedEndpoint{}}
		m.st.owners[owner] = os
	}
	from := os.pool
	ps, created := m.st.pools[id], false
	if ps == nil {
		ps, created = m.newPoolLocked(id), true
	}
	if from != id {
		if old := m.st.pools[from]; old != nil {
			old.owners = slices.DeleteFunc(old.owners, func(o OwnerRef) bool { return o == owner })
		}
		ps.owners = append(ps.owners, owner)
		os.pool = id
	}
	mem.Metrics, mem.Inventory = cloneNames(mem.Metrics), cloneInventory(mem.Inventory)
	os.m = &mem
	os.published = clonePublished(endpoints)
	os.draining = false
	m.applyLocked(ps, created)
	if from != "" && from != id {
		if old := m.st.pools[from]; old != nil {
			m.applyLocked(old, false)
		}
	}
	m.refreshRoutesLocked(ctx)
	return nil
}

// Refuses a membership a pool cannot hold, a user pool serving another model, runtime or accelerator, or an owner speaking another protocol
func (m *Manager) compatibleLocked(ps *poolState, owner OwnerRef, mem *Membership) error {
	if u := ps.user; u != nil {
		if u.GetModel() != mem.Model.String() || (u.GetRuntimeId() != "" && u.GetRuntimeId() != mem.Runtime) || (u.GetAccelerator() != "" && u.GetAccelerator() != mem.Accelerator) {
			return fmt.Errorf("%w: pool %s is a user pool serving %s on %q over %q", ErrConflict, ps.id, u.GetModel(), u.GetRuntimeId(), u.GetAccelerator())
		}
	}
	for _, o := range ps.owners {
		if os := m.st.owners[o]; o != owner && os != nil && os.m != nil && os.m.Protocol != mem.Protocol {
			return fmt.Errorf("%w: pool %s speaks %s to %s %s, not %s", ErrConflict, ps.id, os.m.Protocol, o.Kind, o.ID, mem.Protocol)
		}
	}
	return nil
}

// Flips readiness on every endpoint published for the instance
func (m *Manager) SetReady(ctx context.Context, instanceID string, ready bool) {
	m.setReady(ctx, func(p *Published) bool { return p.InstanceID == instanceID }, ready)
}

// Flips readiness on the endpoints one rank of the instance publishes, leaving its other ranks as they are
func (m *Manager) SetRankReady(ctx context.Context, instanceID string, rank uint32, ready bool) {
	m.setReady(ctx, func(p *Published) bool { return p.InstanceID == instanceID && p.Rank == rank }, ready)
}

// Flips readiness on every published endpoint matching, then plans the pools it touched again
func (m *Manager) setReady(ctx context.Context, match func(*Published) bool, ready bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	touched := map[datalayer.PoolID]bool{}
	for _, os := range m.st.owners {
		for i := range os.published {
			if p := &os.published[i]; match(p) && p.Ready != ready {
				p.Ready = ready
				touched[os.pool] = true
			}
		}
	}
	if len(touched) == 0 {
		return
	}
	for id := range touched {
		if ps := m.st.pools[id]; ps != nil {
			m.applyLocked(ps, false)
		}
	}
	m.refreshRoutesLocked(ctx)
	m.publishViewLocked()
}

// Removes every endpoint the owner published, keeping the owner while it names routes so its pool can wake
func (m *Manager) Unpublish(ctx context.Context, owner OwnerRef) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unpublishLocked(ctx, owner)
	m.publishViewLocked()
}

func (m *Manager) unpublishLocked(ctx context.Context, owner OwnerRef) {
	os := m.st.owners[owner]
	if os == nil || owner.Kind == OwnerUser {
		return
	}
	os.published = nil
	os.draining = false
	ps := m.st.pools[os.pool]
	m.releaseLocked(os)
	if ps != nil {
		ps.waking = false
		m.applyLocked(ps, false)
	}
	m.refreshRoutesLocked(ctx)
}

// Takes every endpoint of the owner out of scheduling and marks its routes draining until it publishes or unpublishes
func (m *Manager) Drain(ctx context.Context, owner OwnerRef) {
	m.mu.Lock()
	defer m.mu.Unlock()
	os := m.st.owners[owner]
	if os == nil || os.draining || owner.Kind == OwnerUser {
		return
	}
	os.draining = true
	if ps := m.st.pools[os.pool]; ps != nil {
		m.applyLocked(ps, false)
	}
	m.refreshRoutesLocked(ctx)
	m.publishViewLocked()
}

// A new empty pool record created now
func (m *Manager) newPoolLocked(id datalayer.PoolID) *poolState {
	now := timestamppb.Now()
	ps := &poolState{id: id, meta: &v1.ObjectMeta{CreatedAt: now, UpdatedAt: now}}
	m.st.pools[id] = ps
	return ps
}

// Plans and commits a pool, deletes it once nothing holds it, and emits the events its changes call for
func (m *Manager) applyLocked(ps *poolState, created bool) {
	if len(ps.owners) == 0 && ps.user == nil {
		m.deletePoolLocked(ps)
		return
	}
	ch := m.commitLocked(ps, m.planLocked(ps))
	statusChanged := m.refreshPoolLocked(ps)
	switch {
	case created:
		m.emitLocked(v1.EventKind_EVENT_KIND_POOL, v1.EventAction_EVENT_ACTION_CREATED, string(ps.id), m.renderPoolLocked(ps))
	case ch.spec || ch.members:
		m.emitLocked(v1.EventKind_EVENT_KIND_POOL, v1.EventAction_EVENT_ACTION_UPDATED, string(ps.id), m.renderPoolLocked(ps))
	case ch.readiness || statusChanged:
		m.coalesceLocked(eventKey{kind: v1.EventKind_EVENT_KIND_POOL, id: string(ps.id)})
	}
}

// Removes a pool, its user endpoints and its record from the store and emits its deletion without the gateway's status entry
func (m *Manager) deletePoolLocked(ps *poolState) {
	var record *v1.Pool
	if ps.record != nil {
		parents := cloneParents(ps.parents)
		conditions.RemoveController(&parents, GatewayController)
		record = m.render(m.poolRecordLocked(ps, ps.conds, parents, ps.ready, ps.record.GetStatus().GetTotalEndpoints()), nil)
	}
	user := OwnerRef{Kind: OwnerUser, ID: string(ps.id)}
	if os := m.st.owners[user]; os != nil {
		m.syncLocked(os, nil)
		delete(m.st.owners, user)
	}
	m.store.DeletePool(ps.id)
	delete(m.st.pools, ps.id)
	if record != nil {
		m.emitLocked(v1.EventKind_EVENT_KIND_POOL, v1.EventAction_EVENT_ACTION_DELETED, string(ps.id), record)
	}
}

// The spec, capabilities and endpoints the pool would hold, writing nothing
func (m *Manager) planLocked(ps *poolState) poolPlan {
	plan := poolPlan{want: map[OwnerRef][]joinedEndpoint{}}
	selector := m.selectorLocked(ps)
	targets := targetPortsLocked(ps)
	ports := map[int]bool{}
	holders := slices.Clone(ps.owners)
	if ps.user != nil {
		holders = append(holders, OwnerRef{Kind: OwnerUser, ID: string(ps.id)})
	}
	for _, o := range holders {
		var want []joinedEndpoint
		if o.Kind == OwnerUser {
			want = staticEndpoints(ps, selector)
		} else {
			want = desiredEndpoints(m.st.owners[o], ps.id, selector, targets)
		}
		for _, w := range want {
			ports[w.spec.Address.Port] = true
		}
		plan.want[o] = want
	}
	plan.spec, plan.caps = m.effectiveLocked(ps, ports)
	return plan
}

// The ports an endpoint must serve on to join, the user's target ports on a pool that has them, nil when any port joins
func targetPortsLocked(ps *poolState) map[int]bool {
	if ps.user == nil || len(ps.user.GetTargetPorts()) == 0 {
		return nil
	}
	out := map[int]bool{}
	for _, p := range ps.user.GetTargetPorts() {
		out[int(p)] = true
	}
	return out
}

// The labels every endpoint must carry, the user's selector on a pool that has one
func (m *Manager) selectorLocked(ps *poolState) map[string]string {
	if ps.user != nil && len(ps.user.GetSelector()) > 0 {
		return ps.user.GetSelector()
	}
	return nil
}

// The spec the pool runs from its oldest local owner, its user spec, or the id a restored route names
func (m *Manager) effectiveLocked(ps *poolState, ports map[int]bool) (*v1.PoolSpec, datalayer.MetricCapabilities) {
	var base *Membership
	for _, o := range localFirst(ps.owners) {
		if os := m.st.owners[o]; os != nil && os.m != nil {
			base = os.m
			break
		}
	}
	derived := len(ps.owners) > 0
	spec := &v1.PoolSpec{}
	var caps datalayer.MetricCapabilities
	switch {
	case base != nil:
		caps = capabilitiesOf(base.Metrics, base.Runtime)
		spec.Model, spec.RuntimeId, spec.Accelerator = base.Model.String(), base.Runtime, base.Accelerator
		spec.AppProtocol = protoProtocol(base.Protocol)
		spec.Concurrency = base.Concurrency
		spec.SchedulerConfig = base.SchedulerConfig
		spec.Inventory = cloneInventory(base.Inventory)
	case derived:
		runtime, model, class, _ := SplitPoolID(ps.id)
		caps = capabilitiesOf(nil, runtime)
		spec.Model, spec.RuntimeId, spec.Accelerator = model, runtime, class
	default:
		spec = proto.Clone(ps.user).(*v1.PoolSpec)
		caps = capabilitiesOf(ps.user.GetMetrics(), ps.user.GetRuntimeId())
	}
	if derived {
		spec.Export = proto.Bool(true)
	}
	if derived && ps.user != nil {
		u := ps.user
		if u.GetFailureMode() != v1.FailureMode_FAILURE_MODE_UNSPECIFIED {
			spec.FailureMode = u.GetFailureMode()
		}
		if u.GetSchedulerConfig() != "" {
			spec.SchedulerConfig = u.GetSchedulerConfig()
		}
		if u.Export != nil {
			spec.Export = proto.Bool(u.GetExport())
		}
		spec.TokensInOut = u.GetTokensInOut()
		spec.Selector = maps.Clone(u.GetSelector())
		spec.StaticEndpoints = cloneStatic(u.GetStaticEndpoints())
	}
	if derived {
		spec.TargetPorts = nil
		for _, p := range slices.Sorted(maps.Keys(ports)) {
			spec.TargetPorts = append(spec.TargetPorts, uint32(p))
		}
		if ps.user != nil && len(ps.user.GetTargetPorts()) > 0 {
			spec.TargetPorts = slices.Clone(ps.user.GetTargetPorts())
		}
	}
	if spec.GetFailureMode() == v1.FailureMode_FAILURE_MODE_UNSPECIFIED {
		spec.FailureMode = v1.FailureMode_FAILURE_MODE_FAIL_CLOSE
	}
	if spec.GetAppProtocol() == v1.AppProtocol_APP_PROTOCOL_UNSPECIFIED {
		spec.AppProtocol = v1.AppProtocol_APP_PROTOCOL_HTTP
	}
	if spec.GetSchedulerConfig() == "" {
		spec.SchedulerConfig = SchedulerNoMetrics
		if caps.Complete() {
			spec.SchedulerConfig = SchedulerDefault
		}
	}
	if len(spec.GetSelector()) == 0 {
		spec.Selector = map[string]string{datalayer.LabelModel: spec.GetModel()}
		if spec.GetRuntimeId() != "" {
			spec.Selector[datalayer.LabelRuntime] = spec.GetRuntimeId()
		}
	}
	spec.Metrics = namesOf(caps)
	return spec, caps
}

// Writes the planned spec and endpoints to the store, bumping the generation on a spec change
func (m *Manager) commitLocked(ps *poolState, plan poolPlan) change {
	var ch change
	if ps.spec == nil || !proto.Equal(ps.spec, plan.spec) {
		ps.meta = proto.Clone(ps.meta).(*v1.ObjectMeta)
		ps.meta.Generation++
		ps.meta.UpdatedAt = timestamppb.Now()
		ps.spec = plan.spec
		ch.spec = true
	}
	ps.dl = datalayerPool(ps.id, plan.spec, plan.caps)
	if ch.spec || !ps.stored {
		m.store.PutPool(ps.dl)
		ps.stored = true
	}
	for _, o := range slices.SortedFunc(maps.Keys(plan.want), compareOwners) {
		os := m.st.owners[o]
		if os == nil {
			os = &ownerState{ref: o, pool: ps.id, joined: map[datalayer.EndpointID]joinedEndpoint{}}
			m.st.owners[o] = os
		}
		members, readiness := m.syncLocked(os, plan.want[o])
		ch.members = ch.members || members
		ch.readiness = ch.readiness || readiness
		if len(os.joined) > 0 {
			ps.waking = false
		}
	}
	return ch
}

// Brings the store's endpoints of one owner to the wanted set, reporting membership and readiness changes apart
func (m *Manager) syncLocked(os *ownerState, want []joinedEndpoint) (members, readiness bool) {
	cur := *m.ports.Load()
	var ports map[datalayer.EndpointID]endpointPorts
	for _, w := range want {
		if p, ok := cur[w.spec.ID]; !ok || p != w.ports {
			if ports == nil {
				ports = maps.Clone(cur)
			}
			ports[w.spec.ID] = w.ports
		}
	}
	if ports != nil {
		m.ports.Store(&ports)
	}
	next := make(map[datalayer.EndpointID]joinedEndpoint, len(want))
	for _, w := range want {
		cur, had := os.joined[w.spec.ID]
		switch {
		case !had || cur.spec.Pool != w.spec.Pool || cur.spec.Address != w.spec.Address || !maps.Equal(cur.spec.Labels, w.spec.Labels):
			members = true
			m.store.UpsertEndpoint(w.spec)
		case cur.spec.Ready != w.spec.Ready:
			readiness = true
			m.store.UpsertEndpoint(w.spec)
		}
		if !had || cur.capacity != w.capacity {
			var capacity any
			if w.capacity > 0 {
				capacity = w.capacity
			}
			m.store.SetAttribute(w.spec.ID, datalayer.CollectorCapacity, capacity)
		}
		m.st.endpoints[w.spec.ID] = os.ref
		next[w.spec.ID] = w
	}
	var left map[datalayer.EndpointID]endpointPorts
	for id := range os.joined {
		if _, keep := next[id]; !keep {
			m.store.DeleteEndpoint(id)
			delete(m.st.endpoints, id)
			if left == nil {
				left = maps.Clone(*m.ports.Load())
			}
			delete(left, id)
			members = true
		}
	}
	if left != nil {
		m.ports.Store(&left)
	}
	os.joined = next
	return members, readiness
}

// Forgets an owner once it publishes nothing and names no route, taking it out of its pool
func (m *Manager) releaseLocked(os *ownerState) {
	if len(os.published) > 0 || m.namesRoutesLocked(os.ref) {
		return
	}
	m.syncLocked(os, nil)
	delete(m.st.owners, os.ref)
	if ps := m.st.pools[os.pool]; ps != nil {
		ps.owners = slices.DeleteFunc(ps.owners, func(o OwnerRef) bool { return o == os.ref })
	}
}

// Whether any route is named by the owner
func (m *Manager) namesRoutesLocked(owner OwnerRef) bool {
	for _, rs := range m.st.routes {
		if rs.owner == owner {
			return true
		}
	}
	return false
}

// The endpoints an owner's published list puts in the pool, those lacking a selector label or serving off the target ports left out
func desiredEndpoints(os *ownerState, pool datalayer.PoolID, selector map[string]string, targetPorts map[int]bool) []joinedEndpoint {
	if os == nil || os.m == nil {
		return nil
	}
	out := make([]joinedEndpoint, 0, len(os.published))
	for i := range os.published {
		p := &os.published[i]
		labels := publishedLabels(os, p)
		if !carries(labels, selector) || (targetPorts != nil && !targetPorts[p.Address.Port]) {
			continue
		}
		out = append(out, joinedEndpoint{
			spec:     datalayer.EndpointSpec{ID: endpointID(p), Pool: pool, Address: p.Address, Labels: labels, Ready: p.Ready && !os.draining},
			ports:    endpointPorts{metrics: int(p.MetricsPort), health: int(p.HealthPort)},
			capacity: int(os.m.Concurrency),
		})
	}
	return out
}

// The labels of a published endpoint, the bridge's own keys written over the publisher's
func publishedLabels(os *ownerState, p *Published) map[string]string {
	labels := maps.Clone(p.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[datalayer.LabelModel] = os.m.Model.String()
	labels[datalayer.LabelRuntime] = os.m.Runtime
	labels[datalayer.LabelInstance] = p.InstanceID
	labels[datalayer.LabelRank] = strconv.FormatUint(uint64(p.Rank), 10)
	setLabel(labels, datalayer.LabelAccelerator, os.m.Accelerator)
	setLabel(labels, datalayer.LabelNode, p.NodeID)
	setLabel(labels, datalayer.LabelRole, p.Role)
	setLabel(labels, datalayer.LabelAdapter, p.Adapter)
	if os.ref.Kind == OwnerFormation {
		labels[datalayer.LabelFormation] = os.ref.ID
	}
	if os.m.KVCapacity > 0 {
		labels[datalayer.LabelKVCapacity] = strconv.FormatUint(uint64(os.m.KVCapacity), 10)
	}
	return labels
}

// Static endpoints of a user spec, ready from the start, labeled with their selector and any KV capacity
func staticEndpoints(ps *poolState, selector map[string]string) []joinedEndpoint {
	u := ps.user
	model, runtime := u.GetModel(), u.GetRuntimeId()
	out := make([]joinedEndpoint, 0, len(u.GetStaticEndpoints()))
	for i, se := range u.GetStaticEndpoints() {
		addr := ps.userAddrs[i]
		labels := maps.Clone(selector)
		if labels == nil {
			labels = map[string]string{}
		}
		labels[datalayer.LabelModel] = model
		labels[datalayer.LabelRank] = strconv.FormatUint(uint64(se.GetRank()), 10)
		setLabel(labels, datalayer.LabelRuntime, runtime)
		setLabel(labels, datalayer.LabelAccelerator, u.GetAccelerator())
		setLabel(labels, datalayer.LabelRole, se.GetRole())
		if se.GetKvCapacity() > 0 {
			labels[datalayer.LabelKVCapacity] = strconv.FormatUint(uint64(se.GetKvCapacity()), 10)
		}
		out = append(out, joinedEndpoint{
			spec:     datalayer.EndpointSpec{ID: staticID(ps.id, se.GetAddress()), Pool: ps.id, Address: addr, Labels: labels, Ready: true},
			capacity: int(u.GetConcurrency()),
		})
	}
	return out
}

// Owners in join order with this node's own ahead of those imported from another member
func localFirst(owners []OwnerRef) []OwnerRef {
	out := slices.Clone(owners)
	slices.SortStableFunc(out, func(a, b OwnerRef) int {
		return cmp.Compare(boolRank(a.Kind == OwnerNode), boolRank(b.Kind == OwnerNode))
	})
	return out
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Orders owners by kind then id
func compareOwners(a, b OwnerRef) int {
	return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.ID, b.ID))
}

func setLabel(labels map[string]string, key, value string) {
	if value != "" {
		labels[key] = value
	}
}

// Whether the labels hold every selector pair
func carries(labels, selector map[string]string) bool {
	for k, v := range selector {
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// The endpoint id of one process port, the instance id slash the port, slash the adapter for an adapter scoped endpoint
func endpointID(p *Published) datalayer.EndpointID {
	id := p.InstanceID + "/" + strconv.Itoa(p.Address.Port)
	if p.Adapter != "" {
		id += "/" + p.Adapter
	}
	return datalayer.EndpointID(id)
}

// The endpoint id of a static endpoint, the pool id slash its address
func staticID(pool datalayer.PoolID, address string) datalayer.EndpointID {
	return datalayer.EndpointID(string(pool) + "/" + address)
}

func validOwner(owner OwnerRef) error {
	switch owner.Kind {
	case OwnerInstance, OwnerSlot, OwnerFormation, OwnerNode:
	case OwnerUser:
		return fmt.Errorf("%w: user pools come through PutPool", ErrInvalid)
	default:
		return fmt.Errorf("%w: owner kind %q", ErrInvalid, owner.Kind)
	}
	if owner.ID == "" {
		return fmt.Errorf("%w: %s owner without an id", ErrInvalid, owner.Kind)
	}
	return nil
}

// Refuses a membership naming no model or runtime or an unknown protocol, an empty protocol meaning HTTP
func validMembership(mem *Membership) error {
	switch {
	case mem.Model.SourceID == "" || mem.Model.Repo == "":
		return fmt.Errorf("%w: model %q names no source and repo", ErrInvalid, mem.Model.String())
	case mem.Runtime == "" || strings.Contains(mem.Runtime, "/"):
		return fmt.Errorf("%w: runtime %q", ErrInvalid, mem.Runtime)
	case strings.Contains(mem.Model.String(), "@"):
		return fmt.Errorf("%w: model %q", ErrInvalid, mem.Model.String())
	}
	switch mem.Protocol {
	case "":
		mem.Protocol = datalayer.AppProtocolHTTP
	case datalayer.AppProtocolHTTP, datalayer.AppProtocolH2C, datalayer.AppProtocolGRPC:
	default:
		return fmt.Errorf("%w: app protocol %q", ErrInvalid, mem.Protocol)
	}
	return nil
}

func validPublished(p *Published) error {
	switch {
	case p.InstanceID == "":
		return fmt.Errorf("%w: endpoint without an instance id", ErrInvalid)
	case p.Address.Host == "" || p.Address.Port < 1 || p.Address.Port > 65535:
		return fmt.Errorf("%w: endpoint %s address %s", ErrInvalid, p.InstanceID, p.Address)
	case p.MetricsPort > 65535 || p.HealthPort > 65535:
		return fmt.Errorf("%w: endpoint %s metrics port %d health port %d", ErrInvalid, p.InstanceID, p.MetricsPort, p.HealthPort)
	}
	return nil
}

// The model key a pool spec names as source colon repo colon group
func modelKeyOf(model string) (ModelKey, bool) {
	parts := strings.SplitN(model, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return ModelKey{}, false
	}
	return ModelKey{SourceID: parts[0], Repo: parts[1], Group: parts[2]}, true
}

func clonePublished(in []Published) []Published {
	out := slices.Clone(in)
	for i := range out {
		out[i].Labels = maps.Clone(out[i].Labels)
	}
	return out
}

func cloneNames(n *v1.MetricNames) *v1.MetricNames {
	if n == nil {
		return nil
	}
	return proto.Clone(n).(*v1.MetricNames)
}

func cloneInventory(inv *v1.RuntimeInventory) *v1.RuntimeInventory {
	if inv == nil {
		return nil
	}
	return proto.Clone(inv).(*v1.RuntimeInventory)
}

func cloneStatic(in []*v1.StaticEndpoint) []*v1.StaticEndpoint {
	out := make([]*v1.StaticEndpoint, len(in))
	for i, se := range in {
		out[i] = proto.Clone(se).(*v1.StaticEndpoint)
	}
	return out
}
