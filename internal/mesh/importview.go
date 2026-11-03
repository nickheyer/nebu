package mesh

import (
	"maps"
	"slices"
	"strings"

	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/conditions"
	"github.com/nickheyer/nebu/pkg/config"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The controller every import entry is written under
const ImportController = "nebu.dev/mesh-import"

const (
	// Imported pools route straight to the exporters' endpoints the exporter's scheduler picks
	RoutingEndpoint = config.RoutingEndpoint
	// Imported pools route through the exporter's gateway
	RoutingParent = config.RoutingParent
)

// Why an import is not ready when its exporters' pool is missing from the pools view
const ReasonPoolNotPublished = "PoolNotPublished"

// The routing mode an imported pool is configured with, its own over the mesh default over endpoint
func (m *Manager) routingMode(poolID string) string {
	cfg := m.Config
	mode := strings.ToLower(strings.TrimSpace(cfg.GetImportRouting()[poolID]))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(cfg.GetRoutingMode()))
	}
	if mode == RoutingParent {
		return RoutingParent
	}
	return RoutingEndpoint
}

// One exporter of a pool as the import view reads it
type exporterView struct {
	node     string
	meshHash string
	address  string
	live     bool
	parents  []conditions.ParentStatus
	draining bool
}

// Every pool imported from other members and meshes by pool id, each with its exporters, endpoints and conditions
func (m *Manager) Imports() []*v1.PoolImport {
	byPool := m.importedBy()
	out := make([]*v1.PoolImport, 0, len(byPool))
	for _, id := range slices.Sorted(maps.Keys(byPool)) {
		out = append(out, m.importOf(id, byPool[id]))
	}
	return out
}

// One imported pool by id, false when nobody exports it here
func (m *Manager) Import(id string) (*v1.PoolImport, bool) {
	byPool := m.importedBy()
	exporters, ok := byPool[id]
	if !ok {
		return nil, false
	}
	return m.importOf(id, exporters), true
}

// The exporters of every imported pool in node order, each with the mesh it exports from and its parent entries
func (m *Manager) importedBy() map[string][]exporterView {
	m.importing.Lock()
	defer m.importing.Unlock()
	byPool := map[string][]exporterView{}
	for exporter, list := range m.imports {
		rec, hash, ok := m.exportSource(exporter)
		if !ok {
			continue
		}
		for id, imp := range list {
			byPool[id] = append(byPool[id], exporterView{node: exporter, meshHash: hash, address: rec.GetAddress(), live: rec.GetState() == v1.NodeState_NODE_STATE_READY, parents: slices.Clone(imp.parents), draining: imp.retiring != nil})
		}
	}
	for id := range byPool {
		slices.SortFunc(byPool[id], func(a, b exporterView) int { return strings.Compare(a.node, b.node) })
	}
	return byPool
}

// The import of one pool, its endpoints those the exporters serve or their gateways in parent mode, with every exporter's parent entries
func (m *Manager) importOf(id string, exporters []exporterView) *v1.PoolImport {
	mode := m.routingMode(id)
	ctl := &v1.ImportController{Name: ImportController}
	for _, e := range exporters {
		ctl.ExportingSources = append(ctl.ExportingSources, &v1.ExportingSource{MeshHash: e.meshHash, NodeId: e.node})
	}
	out := &v1.PoolImport{Id: id, Spec: &v1.PoolImportSpec{RoutingMode: mode}, Status: &v1.PoolImportStatus{Controllers: []*v1.ImportController{ctl}, RoutingMode: mode}}
	pool, ok := m.Pools.Pool(id)
	if !ok {
		ctl.Conditions = []*v1.Condition{m.unpublishedCondition(id)}
	} else {
		m.forgetUnpublished(id)
		out.Meta = proto.Clone(pool.GetMeta()).(*v1.ObjectMeta)
		ctl.Parents, ctl.Conditions = pool.GetStatus().GetParents(), pool.GetStatus().GetConditions()
	}
	for _, e := range exporters {
		ctl.Parents = append(ctl.Parents, protoParents(e.parents)...)
		if mode == RoutingParent {
			out.Status.Endpoints = append(out.Status.Endpoints, &v1.Endpoint{Id: "gateway/" + e.node, PoolId: id, Address: e.address, NodeId: e.node, Ready: e.live && !e.draining, Healthy: e.live})
		}
	}
	if mode == RoutingEndpoint && ok {
		nodes := make([]string, len(exporters))
		for i, e := range exporters {
			nodes[i] = e.node
		}
		for _, ep := range pool.GetStatus().GetEndpoints() {
			if _, found := slices.BinarySearch(nodes, ep.GetNodeId()); found {
				out.Status.Endpoints = append(out.Status.Endpoints, ep)
			}
		}
	}
	return out
}

// The not published condition of an import, its transition time set once when the pool went missing
func (m *Manager) unpublishedCondition(id string) *v1.Condition {
	m.viewing.Lock()
	defer m.viewing.Unlock()
	c, ok := m.unpublished[id]
	if !ok {
		c = &v1.Condition{Type: pools.ConditionReady, Status: v1.ConditionStatus_CONDITION_STATUS_FALSE, Reason: ReasonPoolNotPublished, Message: "the pool view holds no pool of this id", LastTransitionTime: timestamppb.Now()}
		m.unpublished[id] = c
	}
	return proto.Clone(c).(*v1.Condition)
}

// Drops the not published condition of a pool the view holds again
func (m *Manager) forgetUnpublished(id string) {
	m.viewing.Lock()
	defer m.viewing.Unlock()
	delete(m.unpublished, id)
}

// Parent entries as the status package keeps them
func parentsOf(list []*v1.ParentStatus) []conditions.ParentStatus {
	out := make([]conditions.ParentStatus, 0, len(list))
	for _, p := range list {
		ref := p.GetParentRef()
		entry := conditions.ParentStatus{ParentRef: conditions.ParentRef{Group: ref.GetGroup(), Kind: ref.GetKind(), Name: ref.GetName(), Namespace: ref.GetNamespace()}, ControllerName: p.GetControllerName()}
		for _, c := range p.GetConditions() {
			entry.Conditions = append(entry.Conditions, conditionOf(c))
		}
		out = append(out, entry)
	}
	return out
}

// Parent entries as the API carries them
func protoParents(list []conditions.ParentStatus) []*v1.ParentStatus {
	out := make([]*v1.ParentStatus, 0, len(list))
	for _, p := range list {
		entry := &v1.ParentStatus{ParentRef: &v1.ParentReference{Group: p.ParentRef.Group, Kind: p.ParentRef.Kind, Name: p.ParentRef.Name, Namespace: p.ParentRef.Namespace}, ControllerName: p.ControllerName}
		for _, c := range p.Conditions {
			entry.Conditions = append(entry.Conditions, protoCondition(c))
		}
		out = append(out, entry)
	}
	return out
}

// A condition as the status package keeps it
func conditionOf(c *v1.Condition) conditions.Condition {
	status := conditions.Unknown
	switch c.GetStatus() {
	case v1.ConditionStatus_CONDITION_STATUS_TRUE:
		status = conditions.True
	case v1.ConditionStatus_CONDITION_STATUS_FALSE:
		status = conditions.False
	}
	return conditions.Condition{Type: c.GetType(), Status: status, Reason: c.GetReason(), Message: c.GetMessage(), LastTransitionTime: c.GetLastTransitionTime().AsTime(), ObservedGeneration: int64(c.GetObservedGeneration())}
}

// A condition as the API carries it
func protoCondition(c conditions.Condition) *v1.Condition {
	status := v1.ConditionStatus_CONDITION_STATUS_UNKNOWN
	switch c.Status {
	case conditions.True:
		status = v1.ConditionStatus_CONDITION_STATUS_TRUE
	case conditions.False:
		status = v1.ConditionStatus_CONDITION_STATUS_FALSE
	}
	return &v1.Condition{Type: c.Type, Status: status, Reason: c.Reason, Message: c.Message, LastTransitionTime: timestamppb.New(c.LastTransitionTime), ObservedGeneration: uint64(c.ObservedGeneration)}
}
