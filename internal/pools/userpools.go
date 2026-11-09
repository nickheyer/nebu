package pools

import (
	"context"
	"fmt"
	"slices"

	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Creates or replaces a user pool of static endpoints or the user fields of the derived pool of that id
func (m *Manager) PutPool(ctx context.Context, id string, spec *v1.PoolSpec) (*v1.Pool, error) {
	addrs, err := validPoolSpec(id, spec)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pid := datalayer.PoolID(id)
	user := OwnerRef{Kind: OwnerUser, ID: id}
	for i, se := range spec.GetStaticEndpoints() {
		eid := staticID(pid, se.GetAddress())
		if holder, ok := m.st.endpoints[eid]; ok && holder != user {
			return nil, fmt.Errorf("%w: static endpoint %d is published by %s %s", ErrConflict, i, holder.Kind, holder.ID)
		}
	}
	ps, created := m.st.pools[pid], false
	if ps != nil && len(ps.owners) > 0 {
		if ps.spec.GetModel() != spec.GetModel() || (spec.GetRuntimeId() != "" && spec.GetRuntimeId() != ps.spec.GetRuntimeId()) || (spec.GetAccelerator() != "" && spec.GetAccelerator() != ps.spec.GetAccelerator()) {
			return nil, fmt.Errorf("%w: pool %s serves %s on %s over %q", ErrConflict, id, ps.spec.GetModel(), ps.spec.GetRuntimeId(), ps.spec.GetAccelerator())
		}
	}
	if ps == nil {
		ps, created = m.newPoolLocked(pid), true
	}
	prevUser, prevAddrs := ps.user, ps.userAddrs
	ps.user, ps.userAddrs = proto.Clone(spec).(*v1.PoolSpec), addrs
	plan := m.planLocked(ps)
	meta := proto.Clone(ps.meta).(*v1.ObjectMeta)
	if ps.spec == nil || !proto.Equal(ps.spec, plan.spec) {
		meta.Generation++
		meta.UpdatedAt = timestamppb.Now()
	}
	row := &v1.Pool{Id: id, Meta: meta, Spec: ps.user}
	if err := m.db.PutPool(ctx, row); err != nil {
		ps.user, ps.userAddrs = prevUser, prevAddrs
		if created {
			delete(m.st.pools, pid)
		}
		return nil, err
	}
	m.applyLocked(ps, created)
	m.refreshRoutesLocked(ctx)
	m.publishViewLocked()
	return m.renderPoolLocked(ps), nil
}

// Removes a user pool and its static endpoints, refusing a pool an owner's endpoints derive
func (m *Manager) DeletePool(ctx context.Context, id string) (*v1.Pool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ps := m.st.pools[datalayer.PoolID(id)]
	switch {
	case ps == nil || ps.user == nil:
		return nil, fmt.Errorf("%w: user pool %s", ErrNotFound, id)
	case len(ps.owners) > 0:
		return nil, fmt.Errorf("%w: pool %s is derived from %s %s", ErrManaged, id, ps.owners[0].Kind, ps.owners[0].ID)
	}
	if _, err := m.db.DeletePool(ctx, id); err != nil {
		return nil, err
	}
	out := m.renderPoolLocked(ps)
	ps.user, ps.userAddrs = nil, nil
	m.applyLocked(ps, false)
	m.refreshRoutesLocked(ctx)
	m.publishViewLocked()
	return out, nil
}

// Refuses a pool spec the proto rules, static endpoint ports or a missing pool size refuse, returning the addresses
func validPoolSpec(id string, spec *v1.PoolSpec) ([]datalayer.Address, error) {
	switch {
	case id == "":
		return nil, fmt.Errorf("%w: pool without an id", ErrInvalid)
	case spec.GetModel() == "":
		return nil, fmt.Errorf("%w: pool %s names no model", ErrInvalid, id)
	case len(spec.GetTargetPorts()) < 1 || len(spec.GetTargetPorts()) > 8:
		return nil, fmt.Errorf("%w: pool %s has %d target ports, one to eight allowed", ErrInvalid, id, len(spec.GetTargetPorts()))
	}
	for _, p := range spec.GetTargetPorts() {
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("%w: pool %s target port %d", ErrInvalid, id, p)
		}
	}
	if _, ok := v1.AppProtocol_name[int32(spec.GetAppProtocol())]; !ok {
		return nil, fmt.Errorf("%w: pool %s app protocol %d", ErrInvalid, id, spec.GetAppProtocol())
	}
	if _, ok := v1.FailureMode_name[int32(spec.GetFailureMode())]; !ok {
		return nil, fmt.Errorf("%w: pool %s failure mode %d", ErrInvalid, id, spec.GetFailureMode())
	}
	if spec.GetExport() {
		model, ok := modelKeyOf(spec.GetModel())
		if !ok || string(PoolIDOf(model, spec.GetRuntimeId(), spec.GetAccelerator())) != id {
			return nil, fmt.Errorf("%w: exported pool %s must be named runtime slash source colon repo colon group, at accelerator class when one is set, so every member derives it", ErrInvalid, id)
		}
	}
	caps := capabilitiesOf(spec.GetMetrics(), spec.GetRuntimeId())
	sized := caps.SlotsPath != "" || caps.StaticCache
	addrs := make([]datalayer.Address, 0, len(spec.GetStaticEndpoints()))
	seen := map[string]bool{}
	for i, se := range spec.GetStaticEndpoints() {
		addr, err := datalayer.ParseAddress(se.GetAddress())
		if err != nil || addr.Host == "" || addr.Port < 1 || addr.Port > 65535 {
			return nil, fmt.Errorf("%w: pool %s static endpoint %d address %q", ErrInvalid, id, i, se.GetAddress())
		}
		if seen[se.GetAddress()] {
			return nil, fmt.Errorf("%w: pool %s lists static endpoint %s twice", ErrInvalid, id, se.GetAddress())
		}
		seen[se.GetAddress()] = true
		if !slices.Contains(spec.GetTargetPorts(), uint32(addr.Port)) {
			return nil, fmt.Errorf("%w: pool %s static endpoint %s port is not a target port", ErrInvalid, id, se.GetAddress())
		}
		if sized && se.GetKvCapacity() == 0 {
			return nil, fmt.Errorf("%w: pool %s static endpoint %s needs kv_capacity, the KV tokens its whole pool holds, since its metrics state no pool size, given as --static %s,kv-capacity=TOKENS", ErrInvalid, id, se.GetAddress(), se.GetAddress())
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}
