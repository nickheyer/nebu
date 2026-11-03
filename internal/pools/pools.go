// Package pools bridges instances, slots, formations and mesh members into pools, endpoints and routes
package pools

import (
	"context"
	"strings"

	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// Owner kinds a reference names
const (
	OwnerInstance  = "instance"
	OwnerSlot      = "slot"
	OwnerFormation = "formation"
	OwnerNode      = "node"
	OwnerUser      = "user"
)

// What owns a published endpoint or route
type OwnerRef struct {
	// instance, slot, formation, node, or user
	Kind string
	ID   string
}

// Identifies the stored model endpoints of a pool serve
type ModelKey struct {
	SourceID string
	Repo     string
	Group    string
}

// The public name of a model key, source colon repo colon group
func (k ModelKey) String() string { return k.SourceID + ":" + k.Repo + ":" + k.Group }

// One endpoint an owner publishes
type Published struct {
	InstanceID string
	NodeID     string
	Address    datalayer.Address
	Role       string
	Rank       uint32
	Ready      bool
	// The adapter the endpoint serves beside the base model, empty for the base model
	Adapter string
	Labels  map[string]string
	// Ports serving the metrics path and the health path, zero meaning the address port
	MetricsPort uint32
	HealthPort  uint32
}

// The pool an owner's endpoints join, derived from model, runtime and accelerator
type Membership struct {
	Model   ModelKey
	Runtime string
	// The accelerator class the endpoints run on, such as 2x NVIDIA H100 80GB HBM3 or cpu, empty when unknown
	Accelerator string
	Protocol    datalayer.AppProtocol
	// Requests one endpoint runs at once when the runtime reports no capacity
	Concurrency uint32
	// The metric names the runtime emits, nil when it emits none
	Metrics *v1.MetricNames
	// KV cache tokens one endpoint holds for a runtime that reports KV use per slot, zero for one with block gauges
	KVCapacity uint32
	// The scheduler configuration the owner's plan names, empty for the pool default
	SchedulerConfig string
	// Context length, quantization, accelerator, modalities and throughput class the owner's launch reports, nil before it launched
	Inventory *v1.RuntimeInventory
}

// What a route an owner names forwards to
type RouteSpec struct {
	Policy    *v1.Policy
	Profile   *v1.Profile
	Objective string
	// The model name the owner's endpoints answer to, written into each request, the route name when empty
	ServedName string
	// The shape of the formation serving the route and the instance ids of its seats, unset for other owners
	Shape v1.Shape
	Seats []string
	// The node whose gateway answers the route, empty for this node
	NodeID string
}

// One weighted destination a split route sends to
type WeightedTarget struct {
	Pool datalayer.PoolID
	// The model name written into requests, the name the target's endpoints serve
	Model  string
	Weight uint32
}

// Derives the pool id every endpoint of a model, runtime and accelerator class joins across the mesh
func PoolIDOf(model ModelKey, runtime, accelerator string) datalayer.PoolID {
	id := runtime + "/" + model.String()
	if class := AcceleratorClass(accelerator); class != "" {
		id += "@" + class
	}
	return datalayer.PoolID(id)
}

// The accelerator as a pool id carries it, lowercased with every run of other characters folded to one dash
func AcceleratorClass(accelerator string) string {
	var out []rune
	dash := false
	for _, r := range strings.ToLower(accelerator) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.':
			if dash && len(out) > 0 {
				out = append(out, '-')
			}
			dash = false
			out = append(out, r)
		default:
			dash = true
		}
	}
	return string(out)
}

// The runtime, model and accelerator class a derived pool id names, false when the id is not one
func SplitPoolID(id datalayer.PoolID) (runtime, model, class string, ok bool) {
	runtime, rest, found := strings.Cut(string(id), "/")
	if !found || runtime == "" || rest == "" {
		return "", "", "", false
	}
	model, class, _ = strings.Cut(rest, "@")
	return runtime, model, class, model != ""
}

// What instances, slots, formations and the mesh call as their endpoints come and go
type Registrar interface {
	// Replaces every endpoint the owner publishes, creating the pool when it is the first
	Publish(ctx context.Context, owner OwnerRef, m Membership, endpoints []Published) error
	// Marks every endpoint an instance publishes ready or not
	SetReady(ctx context.Context, instanceID string, ready bool)
	// Marks the endpoints one rank of an instance publishes ready or not, its other ranks untouched
	SetRankReady(ctx context.Context, instanceID string, rank uint32, ready bool)
	// Removes every endpoint the owner published, deleting the pool when it was the last
	Unpublish(ctx context.Context, owner OwnerRef)
	// Stops scheduling new requests to every endpoint the owner published while in flight ones finish
	Drain(ctx context.Context, owner OwnerRef)
	// Names a route the owner serves, resolving to the owner's pool
	NameRoute(ctx context.Context, owner OwnerRef, name string, spec RouteSpec) (*v1.Route, error)
	// Points a route the owner named at weighted targets in place of the owner's pool, none leaving it pending
	SplitRoute(ctx context.Context, owner OwnerRef, name string, targets []WeightedTarget) error
	// Removes a route the owner named
	DropRoute(ctx context.Context, owner OwnerRef, name string)
	// Removes every route the owner named
	DropRoutes(ctx context.Context, owner OwnerRef)
	// Holds an objective for the owner, a local one keeping its targets and any other taking the latest
	PutObjectiveFor(ctx context.Context, owner OwnerRef, o *v1.Objective) (*v1.Objective, error)
	// Lets go of one objective the owner held, deleting it once nobody holds it and no route names it
	DropObjective(ctx context.Context, owner OwnerRef, name string)
	// Lets go of every objective the owner held
	DropObjectives(ctx context.Context, owner OwnerRef)
}

// Launches the slot or instance owner of a pool with no endpoints, called for every request that finds the pool empty
type Waker interface {
	Wake(ctx context.Context, owner OwnerRef) error
}

// Supplies the plugin dependencies the gateway builds schedulers with
type SchedulerDeps interface {
	SchedulerDeps() registry.Deps
}

// What the gateway and the API read
type View interface {
	Store() datalayer.Store
	Pools() []*v1.Pool
	Pool(id string) (*v1.Pool, bool)
	Routes() []*v1.Route
	Route(name string) (*v1.Route, bool)
	// The route as the view holds it, shared with every reader and never written, for the request path
	RouteView(name string) (*v1.Route, bool)
	// The pools of every member exported to the mesh, with their endpoints
	Exported() []*v1.Pool
	// The objective of a name, false when none exists
	Objective(name string) (*v1.Objective, bool)
}
