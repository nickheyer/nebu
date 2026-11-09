package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type routeTargetIn struct {
	PoolID string `json:"pool_id" jsonschema:"the pool the share of requests goes to"`
	Model  string `json:"model,omitempty" jsonschema:"the model name written into each request, the pool's model when empty"`
	Weight uint32 `json:"weight" jsonschema:"the target's share of the rule's traffic relative to the others, at least 1"`
}

type routeRuleIn struct {
	Match   string          `json:"match,omitempty" jsonschema:"the exact client model name the rule takes, every request when empty"`
	Targets []routeTargetIn `json:"targets" jsonschema:"weighted pools the rule splits its requests over"`
}

type policyIn struct {
	MaxInFlight       uint32  `json:"max_in_flight,omitempty" jsonschema:"requests in flight at once, the gateway default when zero"`
	RequestsPerSecond float64 `json:"requests_per_second,omitempty" jsonschema:"sustained requests per second, the gateway default when zero"`
	Burst             uint32  `json:"burst,omitempty"`
	RequestTimeoutMs  uint32  `json:"request_timeout_ms,omitempty"`
	UpstreamTimeoutMs uint32  `json:"upstream_timeout_ms,omitempty"`
}

type putRouteIn struct {
	Name          string        `json:"name" jsonschema:"the model name clients send"`
	Rules         []routeRuleIn `json:"rules,omitempty" jsonschema:"rewrite rules in order, the route's current rules when empty"`
	Policy        *policyIn     `json:"policy,omitempty"`
	ToolServers   []string      `json:"tool_servers,omitempty" jsonschema:"tool servers every request on the route gets"`
	MutatingTools bool          `json:"mutating_tools,omitempty"`
	Objective     string        `json:"objective,omitempty" jsonschema:"the objective every request carries unless it names its own"`
}

type staticEndpointIn struct {
	Address    string `json:"address" jsonschema:"host and port of a server nebu does not manage"`
	Role       string `json:"role,omitempty" jsonschema:"prefill or decode for a disaggregated pool, empty otherwise"`
	Rank       uint32 `json:"rank,omitempty"`
	KVCapacity uint32 `json:"kv_capacity,omitempty" jsonschema:"KV cache tokens of the server's whole pool, needed for llama.cpp whose responses state only per slot context"`
}

type putPoolIn struct {
	ID              string             `json:"id" jsonschema:"the pool id routes target"`
	Model           string             `json:"model" jsonschema:"the base model every endpoint serves"`
	RuntimeID       string             `json:"runtime_id,omitempty"`
	Selector        map[string]string  `json:"selector,omitempty" jsonschema:"labels an endpoint carries to join"`
	TargetPorts     []uint32           `json:"target_ports" jsonschema:"ports of each process requests go to, one endpoint per port"`
	AppProtocol     string             `json:"app_protocol,omitempty" jsonschema:"http, h2c, or grpc, http when empty"`
	FailureMode     string             `json:"failure_mode,omitempty" jsonschema:"fail-close or fail-open when the scheduler errors, fail-close when empty"`
	SchedulerConfig string             `json:"scheduler_config,omitempty" jsonschema:"the scheduler configuration by name, the default when empty"`
	StaticEndpoints []staticEndpointIn `json:"static_endpoints,omitempty"`
	Export          *bool              `json:"export,omitempty" jsonschema:"whether every mesh member imports the pool, unset keeping the default"`
	Concurrency     uint32             `json:"concurrency,omitempty" jsonschema:"requests one endpoint runs at once when its runtime reports no capacity"`
}

type putObjectiveIn struct {
	Name     string `json:"name"`
	Priority int32  `json:"priority,omitempty" jsonschema:"band from -1000 to 1000, higher shed later, zero the default band"`
	TtftMs   uint32 `json:"ttft_ms,omitempty" jsonschema:"time to first token the request is routed to meet"`
	TpotMs   uint32 `json:"tpot_ms,omitempty" jsonschema:"time per output token the request is routed to meet"`
}

type putSchedulerConfigIn struct {
	Name         string `json:"name"`
	YAML         string `json:"yaml" jsonschema:"the plugins, profiles and profile handler in YAML"`
	ValidateOnly bool   `json:"validate_only,omitempty" jsonschema:"whether to load the YAML and answer its outcome without storing it"`
}

// A Connect request for an in process call carrying the MCP caller's headers, so trace scoping sees who asks
func carry[T any](extra *mcp.RequestExtra, msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	if extra != nil {
		for k, v := range extra.Header {
			req.Header()[k] = append([]string(nil), v...)
		}
	}
	return req
}

// A request for an in process call once the schema rules accept it, the violations otherwise
func checked[T any, PT interface {
	*T
	proto.Message
}](msg *T) (*connect.Request[T], error) {
	if err := protovalidate.Validate(PT(msg)); err != nil {
		return nil, err
	}
	return connect.NewRequest(msg), nil
}

// A service answer's message, or its error when the call failed
func msgOf[T any, PT interface {
	*T
	proto.Message
}](r *connect.Response[T], err error) (proto.Message, error) {
	if err != nil {
		return nil, err
	}
	return PT(r.Msg), nil
}

// The application protocol a name stands for, http when empty
func appProtocolOf(name string) (v1.AppProtocol, error) {
	switch strings.ToLower(name) {
	case "", "http":
		return v1.AppProtocol_APP_PROTOCOL_HTTP, nil
	case "h2c":
		return v1.AppProtocol_APP_PROTOCOL_H2C, nil
	case "grpc":
		return v1.AppProtocol_APP_PROTOCOL_GRPC, nil
	}
	return 0, fmt.Errorf("app protocol %q is not http, h2c, or grpc", name)
}

// The failure mode a name stands for, fail-close when empty
func failureModeOf(name string) (v1.FailureMode, error) {
	switch strings.ToLower(name) {
	case "", "fail-close":
		return v1.FailureMode_FAILURE_MODE_FAIL_CLOSE, nil
	case "fail-open":
		return v1.FailureMode_FAILURE_MODE_FAIL_OPEN, nil
	}
	return 0, fmt.Errorf("failure mode %q is not fail-close or fail-open", name)
}

// The route spec a put_route call describes, keeping the route's current rules when the call names none
func (n *nebu) routeSpec(ctx context.Context, in putRouteIn) (*v1.RouteSpec, error) {
	spec := &v1.RouteSpec{Objective: in.Objective}
	for _, r := range in.Rules {
		rule := &v1.RewriteRule{Match: r.Match}
		for _, t := range r.Targets {
			rule.Targets = append(rule.Targets, &v1.RewriteTarget{PoolId: t.PoolID, Model: t.Model, Weight: t.Weight})
		}
		spec.Rules = append(spec.Rules, rule)
	}
	if len(in.Rules) == 0 {
		cur, err := n.gateway.GetRoute(ctx, connect.NewRequest(&v1.GetRouteRequest{Name: in.Name}))
		if err != nil {
			return nil, fmt.Errorf("route %s names no rules and has none to keep: %w", in.Name, unwrap(err))
		}
		spec.Rules = cur.Msg.GetRoute().GetSpec().GetRules()
	}
	if p := in.Policy; p != nil {
		spec.Policy = &v1.Policy{MaxInFlight: p.MaxInFlight, RequestsPerSecond: p.RequestsPerSecond, Burst: p.Burst, RequestTimeoutMs: p.RequestTimeoutMs, UpstreamTimeoutMs: p.UpstreamTimeoutMs}
	}
	if len(in.ToolServers) > 0 || in.MutatingTools {
		spec.Profile = &v1.Profile{ToolServers: in.ToolServers, MutatingTools: in.MutatingTools}
	}
	return spec, nil
}

// The pool spec a put_pool call describes
func poolSpec(in putPoolIn) (*v1.PoolSpec, error) {
	protocol, err := appProtocolOf(in.AppProtocol)
	if err != nil {
		return nil, err
	}
	mode, err := failureModeOf(in.FailureMode)
	if err != nil {
		return nil, err
	}
	spec := &v1.PoolSpec{Model: in.Model, RuntimeId: in.RuntimeID, Selector: in.Selector, TargetPorts: in.TargetPorts, AppProtocol: protocol, FailureMode: mode, SchedulerConfig: in.SchedulerConfig, Export: in.Export, Concurrency: in.Concurrency}
	for _, e := range in.StaticEndpoints {
		spec.StaticEndpoints = append(spec.StaticEndpoints, &v1.StaticEndpoint{Address: e.Address, Role: e.Role, Rank: e.Rank, KvCapacity: e.KVCapacity})
	}
	return spec, nil
}

// Adds the tools reading and changing pools, objectives, scheduler configurations and pool imports
func (n *nebu) addPoolTools(s *mcp.Server, ro *mcp.ToolAnnotations) {
	destructive := &mcp.ToolAnnotations{DestructiveHint: ptr(true)}
	mcp.AddTool(s, &mcp.Tool{Name: "list_pools", Description: "Lists pools, the groups of endpoints routes send to, with their endpoints, scraped load, readiness and conditions", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.gateway.ListPools(ctx, connect.NewRequest(&v1.ListPoolsRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "get_pool", Description: "Reads one pool with its spec, endpoints and conditions", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.gateway.GetPool(ctx, connect.NewRequest(&v1.GetPoolRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "put_pool", Description: "Creates or replaces a pool of servers nebu does not manage, or overrides the spec of a pool its instances publish"}, func(ctx context.Context, _ *mcp.CallToolRequest, in putPoolIn) (*mcp.CallToolResult, any, error) {
		spec, err := poolSpec(in)
		if err != nil {
			return nil, nil, err
		}
		req, err := checked(&v1.PutPoolRequest{Id: in.ID, Spec: spec})
		if err != nil {
			return nil, nil, err
		}
		r, err := n.gateway.PutPool(ctx, req)
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "delete_pool", Description: "Removes a pool a user authored", Annotations: destructive}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.gateway.DeletePool(ctx, connect.NewRequest(&v1.DeletePoolRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_pool_imports", Description: "Lists the pools other mesh members export to this node with their exporters, endpoints and conditions", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.imports.ListPoolImports(ctx, connect.NewRequest(&v1.ListPoolImportsRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_objectives", Description: "Lists the named latency objectives and priority bands requests reference by header", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.gateway.ListObjectives(ctx, connect.NewRequest(&v1.ListObjectivesRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "put_objective", Description: "Creates or replaces a named objective with its priority band and time to first token and per output token targets"}, func(ctx context.Context, _ *mcp.CallToolRequest, in putObjectiveIn) (*mcp.CallToolResult, any, error) {
		req, err := checked(&v1.PutObjectiveRequest{Objective: &v1.Objective{Name: in.Name, Priority: in.Priority, TtftMs: in.TtftMs, TpotMs: in.TpotMs}})
		if err != nil {
			return nil, nil, err
		}
		r, err := n.gateway.PutObjective(ctx, req)
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "delete_objective", Description: "Removes an objective no route names", Annotations: destructive}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameIn) (*mcp.CallToolResult, any, error) {
		r, err := n.gateway.DeleteObjective(ctx, connect.NewRequest(&v1.DeleteObjectiveRequest{Name: in.Name}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_scheduler_configs", Description: "Lists scheduler configurations with their YAML and whether each loads", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
		r, err := n.gateway.ListSchedulerConfigs(ctx, connect.NewRequest(&v1.ListSchedulerConfigsRequest{}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "put_scheduler_config", Description: "Creates or replaces a scheduler configuration pools reference by name, or with validate_only loads it and answers whether it is accepted"}, func(ctx context.Context, _ *mcp.CallToolRequest, in putSchedulerConfigIn) (*mcp.CallToolResult, any, error) {
		req, err := checked(&v1.PutSchedulerConfigRequest{Config: &v1.SchedulerConfig{Name: in.Name, Spec: &v1.SchedulerConfigSpec{Yaml: in.YAML}}, ValidateOnly: in.ValidateOnly})
		if err != nil {
			return nil, nil, err
		}
		r, err := n.gateway.PutSchedulerConfig(ctx, req)
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "delete_scheduler_config", Description: "Removes a scheduler configuration that is not built in and no pool runs", Annotations: destructive}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameIn) (*mcp.CallToolResult, any, error) {
		r, err := n.gateway.DeleteSchedulerConfig(ctx, connect.NewRequest(&v1.DeleteSchedulerConfigRequest{Name: in.Name}))
		return answer(r, err)
	})
}

// Adds the pool, objective, scheduler configuration and pool import lists and the pool and route templates
func (n *nebu) addPoolResources(s *mcp.Server, read func(uri, name, description string, fetch func(context.Context) (proto.Message, error))) {
	read("nebu://pools", "pools", "The pools routes send to with their endpoints and conditions", func(ctx context.Context) (proto.Message, error) {
		return msgOf(n.gateway.ListPools(ctx, connect.NewRequest(&v1.ListPoolsRequest{})))
	})
	read("nebu://objectives", "objectives", "The latency objectives and priority bands requests reference", func(ctx context.Context) (proto.Message, error) {
		return msgOf(n.gateway.ListObjectives(ctx, connect.NewRequest(&v1.ListObjectivesRequest{})))
	})
	read("nebu://scheduler-configs", "scheduler configs", "The scheduler configurations pools run", func(ctx context.Context) (proto.Message, error) {
		return msgOf(n.gateway.ListSchedulerConfigs(ctx, connect.NewRequest(&v1.ListSchedulerConfigsRequest{})))
	})
	read("nebu://pool-imports", "pool imports", "The pools other mesh members export to this node", func(ctx context.Context) (proto.Message, error) {
		return msgOf(n.imports.ListPoolImports(ctx, connect.NewRequest(&v1.ListPoolImportsRequest{})))
	})
	n.template(s, "nebu://pools/{id}", "pool", "One pool with its spec, endpoints and conditions", func(ctx context.Context, _ *mcp.ReadResourceRequest, id string) (proto.Message, error) {
		return msgOf(n.gateway.GetPool(ctx, connect.NewRequest(&v1.GetPoolRequest{Id: id})))
	})
	n.template(s, "nebu://routes/{name}", "route", "One route with its rules, policy, objective and conditions", func(ctx context.Context, _ *mcp.ReadResourceRequest, name string) (proto.Message, error) {
		return msgOf(n.gateway.GetRoute(ctx, connect.NewRequest(&v1.GetRouteRequest{Name: name})))
	})
}

// Adds a template whose last path element names the record, answering not found when the fetch fails
func (n *nebu) template(s *mcp.Server, uri, name, description string, fetch func(context.Context, *mcp.ReadResourceRequest, string) (proto.Message, error)) {
	prefix := uri[:strings.LastIndex(uri, "{")]
	s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: uri, Name: name, Description: description, MIMEType: "application/json"}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		msg, err := fetch(ctx, req, strings.TrimPrefix(req.Params.URI, prefix))
		if err != nil {
			if connect.CodeOf(err) == connect.CodeNotFound {
				return nil, mcp.ResourceNotFoundError(req.Params.URI)
			}
			return nil, unwrap(err)
		}
		data, _ := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "application/json", Text: string(data)}}}, nil
	})
}
