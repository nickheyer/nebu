package instances

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"google.golang.org/protobuf/proto"
)

// Labels an instance sets on the endpoints it publishes
const (
	// The slot the instance occupies
	LabelSlot = "slot"
	// The generation modes a diffusion endpoint serves, comma separated
	LabelModes = "modes"
	// The model name the endpoint answers to, what a split route's target selects by
	LabelServed = "served"
)

// Where a stable-diffusion.cpp server lists the generation modes it supports
const sdcppCapabilities = "/sdcpp/v1/capabilities"

// The model name a launch answers to, its served name param, else the formation name, else the instance name
func ServedName(rec *v1.Instance) string {
	params := rec.GetParams()
	if name := strings.TrimSpace(params["served_model_name"]); name != "" {
		return name
	}
	if alias, _, _ := strings.Cut(params["alias"], ","); strings.TrimSpace(alias) != "" {
		return strings.TrimSpace(alias)
	}
	if name := rec.GetSeat().GetName(); name != "" {
		return name
	}
	return rec.GetName()
}

// The owner an instance publishes its endpoints and names its route under
func ownerOf(id string) pools.OwnerRef { return pools.OwnerRef{Kind: pools.OwnerInstance, ID: id} }

// The data layer's spelling of a protocol a runtime names
func protocolOf(p v1.AppProtocol) (datalayer.AppProtocol, error) {
	switch p {
	case v1.AppProtocol_APP_PROTOCOL_HTTP:
		return datalayer.AppProtocolHTTP, nil
	case v1.AppProtocol_APP_PROTOCOL_H2C:
		return datalayer.AppProtocolH2C, nil
	case v1.AppProtocol_APP_PROTOCOL_GRPC:
		return datalayer.AppProtocolGRPC, nil
	}
	return "", fmt.Errorf("the runtime names protocol %s, which the gateway does not speak", p)
}

// The pool membership a launch's endpoints join, refused when the runtime reports KV use against a pool size the launch lacks
func MembershipOf(rt runtimes.Runtime, rec *v1.Instance, schedulerConfig, accelerator string) (pools.Membership, error) {
	params := rec.GetParams()
	protocol, err := protocolOf(rt.AppProtocol(params))
	if err != nil {
		return pools.Membership{}, err
	}
	kv := rt.KVCapacity(params, rec.GetPlan())
	if names := rt.Metrics(); kv == 0 && (names.GetSlotsPath() != "" || names.GetStaticCache()) {
		return pools.Membership{}, fmt.Errorf("%s records no %s and its plan names none, so the size of the KV pool its slots draw from is unknown", rec.GetName(), contextParam(rt))
	}
	return pools.Membership{
		Model:           pools.ModelKey{SourceID: rec.GetSourceId(), Repo: rec.GetRepo(), Group: rec.GetGroup()},
		Runtime:         rec.GetRuntimeId(),
		Accelerator:     accelerator,
		Protocol:        protocol,
		Concurrency:     rt.Capacity(params),
		Metrics:         metricsOf(rt),
		KVCapacity:      kv,
		SchedulerConfig: schedulerConfig,
		Inventory:       rec.GetInventory(),
	}, nil
}

// The param a runtime sizes its context by, as its plan policy names it
func contextParam(rt runtimes.Runtime) string {
	if p := rt.Policy(); p != nil && p.ContextParam != "" {
		return p.ContextParam
	}
	return "context length"
}

// The accelerator class a plan runs on, the device pools it uses named through the host profile, cpu for none, empty without a plan
func AcceleratorOf(plan *v1.MemoryPlan, profile *v1.HostProfile) string {
	if plan == nil {
		return ""
	}
	names := map[string]string{}
	for _, d := range profile.GetDevices() {
		names[d.GetId()] = strings.TrimSpace(cmp.Or(d.GetName(), strings.TrimSpace(d.GetVendor()+" "+d.GetId())))
	}
	for _, mp := range profile.GetPools() {
		if name, ok := names[mp.GetDeviceId()]; ok {
			names[mp.GetId()] = name
		}
	}
	var order []string
	count := map[string]int{}
	for _, pu := range plan.GetPools() {
		if pu.GetKind() != v1.PoolKind_POOL_KIND_DEVICE || pu.GetUsedBytes() == 0 {
			continue
		}
		name := cmp.Or(names[pu.GetPoolId()], pu.GetPoolId())
		if count[name] == 0 {
			order = append(order, name)
		}
		count[name]++
	}
	if len(order) == 0 {
		return "cpu"
	}
	parts := make([]string, len(order))
	for i, name := range order {
		parts[i] = strconv.Itoa(count[name]) + "x " + name
	}
	return strings.Join(parts, " + ")
}

// The accelerator class an instance's plan runs on, named through the host profile when the prober answers
func (m *Manager) AcceleratorOf(ctx context.Context, rec *v1.Instance) string {
	var profile *v1.HostProfile
	if m.Host != nil {
		if p, err := m.Host.Profile(ctx, false); err == nil {
			profile = p
		}
	}
	return AcceleratorOf(rec.GetPlan(), profile)
}

// Every name an instance's route is served under, its name then each comma separated alias param not equal to it
func RouteNames(rec *v1.Instance) []string {
	out := []string{rec.GetName()}
	for alias := range strings.SplitSeq(rec.GetParams()["alias"], ",") {
		if alias = strings.TrimSpace(alias); alias != "" && !slices.Contains(out, alias) {
			out = append(out, alias)
		}
	}
	return out
}

// A copy of the metric names the runtime emits, nil when it emits none
func metricsOf(rt runtimes.Runtime) *v1.MetricNames {
	if mn := rt.Metrics(); mn != nil {
		return proto.Clone(mn).(*v1.MetricNames)
	}
	return nil
}

// The host and port of an endpoint URL
func addressOf(endpoint string) (datalayer.Address, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return datalayer.Address{}, fmt.Errorf("endpoint %q: %w", endpoint, err)
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil {
		return datalayer.Address{}, fmt.Errorf("endpoint %q: %w", endpoint, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return datalayer.Address{}, fmt.Errorf("endpoint %q: %w", endpoint, err)
	}
	return datalayer.Address{Host: host, Port: port}, nil
}

// One launch's endpoint, the gRPC port when the runtime speaks gRPC, with metrics and health on the HTTP port
func ServingEndpoint(rt runtimes.Runtime, params map[string]string, instanceID string, http datalayer.Address) (pools.Published, error) {
	ep := pools.Published{
		InstanceID:  instanceID,
		Address:     http,
		Ready:       true,
		MetricsPort: uint32(rt.MetricsPort(params, http.Port)),
		HealthPort:  uint32(http.Port),
	}
	if rt.AppProtocol(params) == v1.AppProtocol_APP_PROTOCOL_GRPC {
		grpc := rt.GRPCPort(params, http.Port)
		if grpc == 0 {
			return pools.Published{}, fmt.Errorf("%s speaks gRPC with these params and names no gRPC port", rt.ID())
		}
		ep.Address.Port = grpc
	}
	return ep, nil
}

// Every endpoint a ready instance serves, each rank's serving port then that port scoped to each adapter the rank applies
func (m *Manager) endpoints(ctx context.Context, in *instance) ([]pools.Published, error) {
	rec := in.snapshot()
	labels := map[string]string{LabelServed: ServedName(rec)}
	if rec.GetSlotId() != "" {
		labels[LabelSlot] = rec.GetSlotId()
	}
	if modes := in.modesOf(); len(modes) > 0 {
		labels[LabelModes] = strings.Join(modes, ",")
	}
	refs := rankRefs(rec)
	var out []pools.Published
	for _, ref := range refs {
		http, err := addressOf(ref.endpoint)
		if err != nil {
			return nil, err
		}
		base, err := ServingEndpoint(in.rt, rec.GetParams(), rec.GetId(), http)
		if err != nil {
			return nil, err
		}
		base.Rank = PublishedRank(rec.GetSeat(), len(refs), ref.rank)
		base.Ready = !in.rankDown(ref.rank)
		base.Labels = kvEventLabels(in.rt, rec.GetParams(), http, ref.rank, maps.Clone(labels))
		out = append(out, base)
		client := in.rt.Adapters(ref.endpoint, rec.GetParams())
		if client == nil {
			continue
		}
		adapters, err := client.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("adapters of %s rank %d: %w", rec.GetName(), ref.rank, err)
		}
		for _, a := range adapters {
			scoped := base
			scoped.Adapter = a.Name
			scoped.Labels = maps.Clone(base.Labels)
			scoped.Labels[datalayer.LabelAdapter] = a.Name
			out = append(out, scoped)
		}
	}
	return out, nil
}

// Publishes a ready instance's endpoints under its own owner, seats left to their conductor
func (m *Manager) publish(ctx context.Context, in *instance) error {
	rec := in.snapshot()
	if rec.GetSeat() != nil {
		return nil
	}
	membership, err := MembershipOf(in.rt, rec, "", m.AcceleratorOf(ctx, rec))
	if err != nil {
		return err
	}
	eps, err := m.endpoints(ctx, in)
	if err != nil {
		return err
	}
	if err := m.Registrar.Publish(ctx, ownerOf(rec.GetId()), membership, eps); err != nil {
		return fmt.Errorf("publish %s: %w", rec.GetName(), err)
	}
	in.setPublished(true)
	return nil
}

// Removes the instance's endpoints from its pool
func (m *Manager) unpublish(ctx context.Context, in *instance) {
	if !in.setPublished(false) {
		return
	}
	m.Registrar.Unpublish(ctx, ownerOf(in.id()))
}

// Declares the instance's pool with no endpoints and names a route per name and alias, slot and seat instances naming none
func (m *Manager) register(ctx context.Context, in *instance, params map[string]string) error {
	rec := in.snapshot()
	if rec.GetSeat() != nil {
		return nil
	}
	declared := proto.Clone(rec).(*v1.Instance)
	if len(declared.GetParams()) == 0 {
		declared.Params = params
	}
	membership, err := MembershipOf(in.rt, declared, "", m.AcceleratorOf(ctx, declared))
	if err != nil {
		return err
	}
	owner := ownerOf(rec.GetId())
	if !in.isPublished() {
		if err := m.Registrar.Publish(ctx, owner, membership, nil); err != nil {
			return fmt.Errorf("pool of %s: %w", rec.GetName(), err)
		}
	}
	if rec.GetSlotId() != "" {
		return nil
	}
	req := rec.GetRequest()
	spec := pools.RouteSpec{Policy: req.GetPolicy(), Profile: req.GetRouteProfile(), ServedName: ServedName(declared)}
	for _, name := range RouteNames(declared) {
		if _, err := m.Registrar.NameRoute(ctx, owner, name, spec); err != nil {
			return fmt.Errorf("route %s: %w", name, err)
		}
	}
	return nil
}

// Drops the route and pool claim an instance holds once it will not serve again
func (m *Manager) dropRoute(ctx context.Context, in *instance) {
	rec := in.snapshot()
	if rec.GetSeat() != nil {
		return
	}
	m.Registrar.DropRoutes(ctx, ownerOf(rec.GetId()))
	m.Registrar.Unpublish(ctx, ownerOf(rec.GetId()))
}

// Reads the img_gen and vid_gen modes a stable-diffusion.cpp server supports
func diffusionModes(ctx context.Context, endpoint string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(endpoint, "/")+sdcppCapabilities, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("capabilities answered %d", resp.StatusCode)
	}
	var caps struct {
		SupportedModes []string `json:"supported_modes"`
	}
	if err := json.Unmarshal(raw, &caps); err != nil {
		return nil, fmt.Errorf("capabilities: %w", err)
	}
	if len(caps.SupportedModes) == 0 {
		return nil, fmt.Errorf("capabilities list no generation mode")
	}
	return caps.SupportedModes, nil
}

// Adds the KV event and replay addresses of a rank to its labels when its runtime publishes them
func kvEventLabels(rt runtimes.Runtime, params map[string]string, http datalayer.Address, rank int, labels map[string]string) map[string]string {
	pub, ok := rt.(runtimes.KVEventPublisher)
	if !ok {
		return labels
	}
	events, replay := pub.KVEvents(params, http.Host, http.Port, rank)
	if events == "" {
		return labels
	}
	labels[datalayer.LabelKVEvents] = events
	labels[datalayer.LabelKVReplay] = replay
	return labels
}
