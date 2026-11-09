package cli

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/pools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Largest weight a rewrite target takes
const maxWeight = 1000000

func poolCommands() command {
	return command{name: "pools", summary: "groups of endpoints routes send requests to", run: runPoolsList, sub: []command{
		{name: "list", summary: "list pools with their ready endpoints", run: runPoolsList},
		{name: "show", summary: "show a pool's spec, conditions, and endpoints", run: runPoolsShow},
		{name: "put", summary: "create a pool or change the settings given on one", run: runPoolsPut},
		{name: "delete", summary: "remove a pool a user authored", run: runPoolsDelete},
	}}
}

func routeCommands() command {
	return command{name: "routes", summary: "gateway model names and the weighted pools they send to", run: runRoutesList, sub: []command{
		{name: "list", summary: "list routes with their pools, counters, and limits", run: runRoutesList},
		{name: "show", summary: "show a route's rules, limits, profile, objective, and conditions", run: runRoutesShow},
		{name: "put", summary: "create a route or change the settings given on one", run: runRoutesPut},
		{name: "delete", summary: "remove a route a user authored", run: runRoutesDelete},
	}}
}

func objectiveCommands() command {
	return command{name: "objectives", summary: "latency objectives and priority bands requests name", run: runObjectivesList, sub: []command{
		{name: "list", summary: "list objectives", run: runObjectivesList},
		{name: "put", summary: "create or replace an objective", run: runObjectivesPut},
		{name: "delete", summary: "remove an objective no route names", run: runObjectivesDelete},
	}}
}

func schedulerCommands() command {
	return command{name: "schedulers", summary: "scheduler configurations pools run requests through", run: runSchedulersList, sub: []command{
		{name: "list", summary: "list scheduler configurations and whether each loads", run: runSchedulersList},
		{name: "show", summary: "print a configuration's YAML and conditions", run: runSchedulersShow},
		{name: "put", summary: "create or replace a configuration from a YAML file", run: runSchedulersPut},
		{name: "validate", summary: "load a YAML file as the daemon would, storing nothing", run: runSchedulersValidate},
		{name: "delete", summary: "remove a configuration no pool runs", run: runSchedulersDelete},
	}}
}

func runPoolsList(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("pools list"), args, 0, 0, "pools list"); err != nil {
		return err
	}
	resp, err := e.cl.gateway.ListPools(ctx, connect.NewRequest(&v1.ListPoolsRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { poolsTable(w, resp.Msg.GetPools()) })
}

// One row per pool with its owner, model, ready endpoints, and how the gateway reaches them
func poolsTable(w io.Writer, list []*v1.Pool) {
	var rows [][]string
	for _, p := range list {
		s, st := p.GetSpec(), p.GetStatus()
		rows = append(rows, []string{p.GetId(), ownerText(p.GetOwnerKind(), p.GetOwnerId()), s.GetModel(), orDash(s.GetRuntimeId()), fmt.Sprintf("%d/%d", st.GetReadyEndpoints(), st.GetTotalEndpoints()), portsText(s.GetTargetPorts()), enumShort(s.GetAppProtocol()), enumShort(s.GetFailureMode()), orDash(s.GetSchedulerConfig()), yes(s.GetExport())})
	}
	table(w, []string{"ID", "OWNER", "MODEL", "RUNTIME", "READY", "PORTS", "PROTOCOL", "FAILURE MODE", "SCHEDULER", "EXPORT"}, rows)
}

func runPoolsShow(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("pools show"), args, 1, 1, "pools show <id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.gateway.GetPool(ctx, connect.NewRequest(&v1.GetPoolRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { renderPool(w, resp.Msg.GetPool()) })
}

// Prints a pool's spec, its conditions, and every endpoint with its scraped load
func renderPool(w io.Writer, p *v1.Pool) {
	s, st := p.GetSpec(), p.GetStatus()
	fmt.Fprintf(w, "%s owned by %s\n", p.GetId(), ownerText(p.GetOwnerKind(), p.GetOwnerId()))
	table(w, nil, [][]string{
		{"model", s.GetModel()},
		{"runtime", orDash(s.GetRuntimeId())},
		{"selector", orDash(joinPairs(s.GetSelector()))},
		{"ports", portsText(s.GetTargetPorts())},
		{"protocol", enumShort(s.GetAppProtocol())},
		{"failure mode", enumShort(s.GetFailureMode())},
		{"scheduler config", orDash(s.GetSchedulerConfig())},
		{"export", yes(s.GetExport())},
		{"concurrency", countText(s.GetConcurrency())},
		{"metric names", orDash(joinPairs(setFields(s.GetMetrics())))},
		{"endpoints ready", fmt.Sprintf("%d of %d", st.GetReadyEndpoints(), st.GetTotalEndpoints())},
		{"generation", fmt.Sprintf("%d, observed %d", p.GetMeta().GetGeneration(), st.GetObservedGeneration())},
	})
	if len(s.GetStaticEndpoints()) > 0 {
		section(w, "static endpoints")
		var rows [][]string
		for _, se := range s.GetStaticEndpoints() {
			rows = append(rows, []string{se.GetAddress(), orDash(se.GetRole()), strconv.Itoa(int(se.GetRank())), countText(se.GetKvCapacity())})
		}
		table(w, []string{"ADDRESS", "ROLE", "RANK", "KV CAPACITY"}, rows)
	}
	section(w, "conditions")
	conditionsTable(w, st.GetConditions())
	for _, ps := range st.GetParents() {
		ref := ps.GetParentRef()
		section(w, fmt.Sprintf("conditions from %s for %s %s", ps.GetControllerName(), ref.GetKind(), ref.GetName()))
		conditionsTable(w, ps.GetConditions())
	}
	section(w, "endpoints")
	endpointsTable(w, st.GetEndpoints())
}

// One row per endpoint with its placement, readiness, and scraped load
func endpointsTable(w io.Writer, list []*v1.Endpoint) {
	var rows [][]string
	for _, ep := range list {
		queued, running, kv := "-", "-", "-"
		if m := ep.GetMetrics(); m != nil {
			queued, running, kv = strconv.Itoa(int(m.GetQueuedRequests())), strconv.Itoa(int(m.GetRunningRequests())), fmt.Sprintf("%.0f%%", m.GetKvCacheUtilization()*100)
		}
		rows = append(rows, []string{ep.GetAddress(), shortID(ep.GetNodeId()), orDash(ep.GetInstanceId()), orDash(ep.GetRole()), strconv.Itoa(int(ep.GetRank())), yes(ep.GetReady()), yes(ep.GetHealthy()), queued, running, kv, strconv.Itoa(int(ep.GetAssumedRequests()))})
	}
	table(w, []string{"ADDRESS", "NODE", "INSTANCE", "ROLE", "RANK", "READY", "HEALTHY", "QUEUED", "RUNNING", "KV", "ASSUMED"}, rows)
}

// Prints conditions with their status, reason, and when they last changed
func conditionsTable(w io.Writer, conds []*v1.Condition) {
	var rows [][]string
	for _, c := range conds {
		rows = append(rows, []string{c.GetType(), loud(c.GetStatus()), orDash(c.GetReason()), when(c.GetLastTransitionTime(), time.RFC3339), c.GetMessage()})
	}
	table(w, []string{"TYPE", "STATUS", "REASON", "CHANGED", "MESSAGE"}, rows)
}

// The condition of the type, nil when absent
func conditionOf(conds []*v1.Condition, typ string) *v1.Condition {
	for _, c := range conds {
		if c.GetType() == typ {
			return c
		}
	}
	return nil
}

// The resource that owns a pool or route as kind/id, user for one a person authored
func ownerText(kind, id string) string {
	if kind == "" {
		return "user"
	}
	return kind + "/" + id
}

func portsText(ports []uint32) string {
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = strconv.Itoa(int(p))
	}
	return orDash(strings.Join(parts, ","))
}

func countText(n uint32) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(int(n))
}

// Every set field of a flat message by proto name, none for a nil message
func setFields(m proto.Message) map[string]string {
	out := map[string]string{}
	if !m.ProtoReflect().IsValid() {
		return out
	}
	m.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		out[string(fd.Name())] = v.String()
		return true
	})
	return out
}

func runPoolsPut(ctx context.Context, e *env, args []string) error {
	fs := e.flags("pools put")
	file := fs.String("file", "", "a YAML or JSON PoolSpec the other flags apply over, the pool's current spec when unset")
	apply := poolFlags(fs)
	positional, err := e.parse(fs, args, 1, 1, "pools put <id> [--file SPEC] [--model M] [--runtime R] [--port N]... [--protocol http|h2c|grpc] [--failure-mode fail-close|fail-open] [--scheduler-config NAME] [--static ADDR[,role=R][,rank=N][,kv-capacity=TOKENS]]... [--export] [--concurrency N] [--selector k=v]...")
	if err != nil {
		return err
	}
	spec, err := e.basePoolSpec(ctx, positional[0], *file)
	if err != nil {
		return err
	}
	if err := apply(spec); err != nil {
		return err
	}
	resp, err := e.cl.gateway.PutPool(ctx, connect.NewRequest(&v1.PutPoolRequest{Id: positional[0], Spec: spec}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { renderPool(w, resp.Msg.GetPool()) })
}

// The spec flags apply over, the file's when given, else the pool's current one, else an empty one
func (e *env) basePoolSpec(ctx context.Context, id, file string) (*v1.PoolSpec, error) {
	spec := &v1.PoolSpec{}
	if file != "" {
		return spec, readMessage(file, spec)
	}
	cur, err := e.cl.gateway.GetPool(ctx, connect.NewRequest(&v1.GetPoolRequest{Id: id}))
	switch {
	case err == nil:
		proto.Merge(spec, cur.Msg.GetPool().GetSpec())
		return spec, nil
	case connect.CodeOf(err) == connect.CodeNotFound:
		return spec, nil
	}
	return nil, err
}

// Defines pool spec flags and returns a function setting the given ones on a spec
func poolFlags(fs *flag.FlagSet) func(*v1.PoolSpec) error {
	var ports, statics, selector multi
	var concurrency u32
	model := fs.String("model", "", "the base model every endpoint serves")
	runtimeID := fs.String("runtime", "", "the runtime endpoints run")
	fs.Var(&ports, "port", "port requests go to on each process, repeatable, replacing the current ports")
	protocol := fs.String("protocol", "", "how the gateway speaks to endpoints: http, h2c, or grpc")
	failure := fs.String("failure-mode", "", "what requests do when the scheduler fails: fail-close or fail-open")
	scheduler := fs.String("scheduler-config", "", "the scheduler configuration requests run through, by name")
	fs.Var(&statics, "static", "server nebu does not manage as ADDR[,role=ROLE][,rank=N][,kv-capacity=TOKENS], repeatable, replacing the current list")
	export := fs.Bool("export", false, "offer the pool to every member of the mesh")
	fs.Var(&concurrency, "concurrency", "`count` of requests one endpoint runs at once when its runtime reports no capacity")
	fs.Var(&selector, "selector", "label an endpoint carries to join as name=value, repeatable, replacing the current selector")
	return func(spec *v1.PoolSpec) error {
		set := given(fs)
		if set["model"] {
			spec.Model = *model
		}
		if set["runtime"] {
			spec.RuntimeId = *runtimeID
		}
		if set["port"] {
			spec.TargetPorts = nil
			for _, p := range ports {
				n, err := parsePort(p)
				if err != nil {
					return err
				}
				spec.TargetPorts = append(spec.TargetPorts, n)
			}
		}
		if set["protocol"] {
			n, err := enumNamed(v1.AppProtocol(0).Descriptor(), *protocol, "protocol")
			if err != nil {
				return err
			}
			spec.AppProtocol = v1.AppProtocol(n)
		}
		if set["failure-mode"] {
			n, err := enumNamed(v1.FailureMode(0).Descriptor(), *failure, "failure mode")
			if err != nil {
				return err
			}
			spec.FailureMode = v1.FailureMode(n)
		}
		if set["scheduler-config"] {
			spec.SchedulerConfig = *scheduler
		}
		if set["static"] {
			spec.StaticEndpoints = nil
			for _, s := range statics {
				se, err := parseStatic(s)
				if err != nil {
					return err
				}
				spec.StaticEndpoints = append(spec.StaticEndpoints, se)
			}
		}
		if set["export"] {
			spec.Export = proto.Bool(*export)
		}
		if set["concurrency"] {
			spec.Concurrency = uint32(concurrency)
		}
		if set["selector"] {
			labels, err := pairs(selector, "selector")
			if err != nil {
				return err
			}
			spec.Selector = labels
		}
		return nil
	}
}

// A target port from 1 to 65535
func parsePort(s string) (uint32, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("port %q is not a whole number from 1 to 65535", s)
	}
	return uint32(n), nil
}

// Reads ADDR[,role=ROLE][,rank=N][,kv-capacity=TOKENS] into a static endpoint
func parseStatic(s string) (*v1.StaticEndpoint, error) {
	parts := strings.Split(s, ",")
	se := &v1.StaticEndpoint{Address: strings.TrimSpace(parts[0])}
	if _, _, err := net.SplitHostPort(se.Address); err != nil {
		return nil, fmt.Errorf("static endpoint %q: %w", s, err)
	}
	for _, opt := range parts[1:] {
		k, v, _ := strings.Cut(strings.TrimSpace(opt), "=")
		switch k {
		case "role":
			se.Role = v
		case "rank":
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("static endpoint %q: rank %q is not a whole number", s, v)
			}
			se.Rank = uint32(n)
		case "kv-capacity":
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil || n == 0 {
				return nil, fmt.Errorf("static endpoint %q: kv-capacity %q is not a positive whole number of tokens", s, v)
			}
			se.KvCapacity = uint32(n)
		default:
			return nil, fmt.Errorf("static endpoint %q: expected ADDR[,role=ROLE][,rank=N][,kv-capacity=TOKENS]", s)
		}
	}
	return se, nil
}

func runPoolsDelete(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("pools delete"), args, 1, 1, "pools delete <id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.gateway.DeletePool(ctx, connect.NewRequest(&v1.DeletePoolRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "deleted %s\n", resp.Msg.GetPool().GetId()) })
}

// The gateway's default limits routes inherit
func (e *env) defaultPolicy(ctx context.Context) (*v1.Policy, error) {
	resp, err := e.cl.gateway.GetGatewayStatus(ctx, connect.NewRequest(&v1.GetGatewayStatusRequest{}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetStatus().GetPolicy(), nil
}

func runRoutesList(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("routes list"), args, 0, 0, "routes list"); err != nil {
		return err
	}
	resp, err := e.cl.gateway.ListRoutes(ctx, connect.NewRequest(&v1.ListRoutesRequest{}))
	if err != nil {
		return err
	}
	defaults, err := e.defaultPolicy(ctx)
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { routesTable(w, resp.Msg.GetRoutes(), defaults) })
}

// One row per route with its owner, resolved pools, counters, effective limits, and objective
func routesTable(w io.Writer, list []*v1.Route, defaults *v1.Policy) {
	var rows [][]string
	for _, r := range list {
		st, spec := r.GetStatus(), r.GetSpec()
		rows = append(rows, []string{r.GetName(), loud(st.GetState()), ownerText(r.GetOwnerKind(), r.GetOwnerId()), orDash(strings.Join(st.GetPoolIds(), ",")), strconv.FormatUint(st.GetRequests(), 10), strconv.Itoa(int(st.GetInFlight())), policyText(inherit(spec.GetPolicy(), defaults)), orDash(spec.GetObjective())})
	}
	table(w, []string{"NAME", "STATE", "OWNER", "POOLS", "REQUESTS", "IN FLIGHT", "POLICY", "OBJECTIVE"}, rows)
}

// Fills every limit the route leaves at zero from the gateway default
func inherit(p, defaults *v1.Policy) *v1.Policy {
	return &v1.Policy{
		MaxInFlight:       cmp.Or(p.GetMaxInFlight(), defaults.GetMaxInFlight()),
		RequestsPerSecond: cmp.Or(p.GetRequestsPerSecond(), defaults.GetRequestsPerSecond()),
		Burst:             cmp.Or(p.GetBurst(), defaults.GetBurst()),
		RequestTimeoutMs:  cmp.Or(p.GetRequestTimeoutMs(), defaults.GetRequestTimeoutMs()),
		UpstreamTimeoutMs: cmp.Or(p.GetUpstreamTimeoutMs(), defaults.GetUpstreamTimeoutMs()),
	}
}

func runRoutesShow(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("routes show"), args, 1, 1, "routes show <name>")
	if err != nil {
		return err
	}
	resp, err := e.cl.gateway.GetRoute(ctx, connect.NewRequest(&v1.GetRouteRequest{Name: positional[0]}))
	if err != nil {
		return err
	}
	defaults, err := e.defaultPolicy(ctx)
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { renderRoute(w, resp.Msg.GetRoute(), defaults) })
}

// Prints a route's settings, its rules with each target's share, and its conditions
func renderRoute(w io.Writer, r *v1.Route, defaults *v1.Policy) {
	st, spec := r.GetStatus(), r.GetSpec()
	node := r.GetNodeId()
	if node == "" {
		node = "this node"
	}
	fmt.Fprintf(w, "%s %s\n", r.GetName(), loud(st.GetState()))
	table(w, nil, [][]string{
		{"owner", ownerText(r.GetOwnerKind(), r.GetOwnerId())},
		{"answered by", node},
		{"pools", orDash(strings.Join(st.GetPoolIds(), ", "))},
		{"requests", strconv.FormatUint(st.GetRequests(), 10)},
		{"in flight", strconv.Itoa(int(st.GetInFlight()))},
		{"policy", policyText(spec.GetPolicy())},
		{"effective policy", policyText(inherit(spec.GetPolicy(), defaults))},
		{"profile", profileText(spec.GetProfile())},
		{"objective", orDash(spec.GetObjective())},
		{"generation", fmt.Sprintf("%d, observed %d", r.GetMeta().GetGeneration(), st.GetObservedGeneration())},
	})
	section(w, "rules")
	rulesTable(w, spec.GetRules())
	section(w, "conditions")
	conditionsTable(w, st.GetConditions())
}

// One row per weighted target with its share of its rule's traffic, a star matching every model
func rulesTable(w io.Writer, rules []*v1.RewriteRule) {
	var rows [][]string
	for _, rule := range rules {
		var total uint64
		for _, t := range rule.GetTargets() {
			total += uint64(t.GetWeight())
		}
		match := rule.GetMatch()
		if match == "" {
			match = "*"
		}
		for _, t := range rule.GetTargets() {
			share := "-"
			if total > 0 {
				share = fmt.Sprintf("%.0f%%", float64(t.GetWeight())*100/float64(total))
			}
			rows = append(rows, []string{match, t.GetPoolId(), orDash(t.GetModel()), strconv.Itoa(int(t.GetWeight())), share})
		}
	}
	table(w, []string{"MATCH", "POOL", "MODEL", "WEIGHT", "SHARE"}, rows)
}

func runRoutesPut(ctx context.Context, e *env, args []string) error {
	fs := e.flags("routes put")
	file := fs.String("file", "", "a YAML or JSON RouteSpec the other flags apply over, the route's current spec when unset")
	var targets multi
	fs.Var(&targets, "target", "weighted destination as POOL[=MODEL][:WEIGHT], repeatable, weight 1 when no trailing :N")
	match := fs.String("match", "", "the client model name the rule of the targets takes, every request when empty")
	objective := fs.String("objective", "", "the objective requests carry unless they name their own")
	policy, readPolicy := policyFlags(fs)
	profile, readProfile := profileFlags(fs)
	positional, err := e.parse(fs, args, 1, 1, "routes put <name> [--file SPEC] [--target POOL[=MODEL][:WEIGHT]]... [--match NAME] [--objective NAME] [--max-in-flight N] [--rps R] [--burst N] [--timeout D] [--upstream-timeout D] [--tool-server REF]... [--mutating-tools]")
	if err != nil {
		return err
	}
	set := given(fs)
	if set["match"] && !set["target"] {
		return errors.New("--match names the rule --target fills, pass at least one --target")
	}
	var rule *v1.RewriteRule
	if set["target"] {
		rule = &v1.RewriteRule{Match: *match}
		for _, s := range targets {
			t, err := parseTarget(s)
			if err != nil {
				return err
			}
			rule.Targets = append(rule.Targets, t)
		}
	}
	if err := readPolicy(); err != nil {
		return err
	}
	if err := readProfile(); err != nil {
		return err
	}
	spec, err := e.baseRouteSpec(ctx, positional[0], *file)
	if err != nil {
		return err
	}
	if rule != nil {
		spec.Rules = upsertRule(spec.GetRules(), rule)
	}
	if set["objective"] {
		spec.Objective = *objective
	}
	if spec.Policy == nil {
		spec.Policy = &v1.Policy{}
	}
	mergePolicy(set, spec.Policy, policy)
	if spec.Profile == nil {
		spec.Profile = &v1.Profile{}
	}
	mergeProfile(set, spec.Profile, profile)
	if err := gateway.CheckProfile(spec.GetProfile()); err != nil {
		return err
	}
	resp, err := e.cl.gateway.PutRoute(ctx, connect.NewRequest(&v1.PutRouteRequest{Name: positional[0], Spec: spec}))
	if err != nil {
		return err
	}
	defaults, err := e.defaultPolicy(ctx)
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { renderRoute(w, resp.Msg.GetRoute(), defaults) })
}

// The spec flags apply over, the file's when given, else the route's current one, else an empty one
func (e *env) baseRouteSpec(ctx context.Context, name, file string) (*v1.RouteSpec, error) {
	spec := &v1.RouteSpec{}
	if file != "" {
		return spec, readMessage(file, spec)
	}
	cur, err := e.cl.gateway.GetRoute(ctx, connect.NewRequest(&v1.GetRouteRequest{Name: name}))
	switch {
	case err == nil:
		proto.Merge(spec, cur.Msg.GetRoute().GetSpec())
		return spec, nil
	case connect.CodeOf(err) == connect.CodeNotFound:
		return spec, nil
	}
	return nil, err
}

// Reads a pool id, an optional equals and model, and an optional trailing colon and weight that defaults to one
func parseTarget(s string) (*v1.RewriteTarget, error) {
	t := &v1.RewriteTarget{Weight: 1}
	rest := strings.TrimSpace(s)
	if i := strings.LastIndexByte(rest, ':'); i >= 0 && digits(rest[i+1:]) {
		w, err := strconv.ParseUint(rest[i+1:], 10, 32)
		if err != nil || w < 1 || w > maxWeight {
			return nil, fmt.Errorf("target %q: weight %s is not from 1 to %d", s, rest[i+1:], maxWeight)
		}
		t.Weight, rest = uint32(w), rest[:i]
	}
	pool, model, named := strings.Cut(rest, "=")
	if pool == "" {
		return nil, fmt.Errorf("target %q names no pool, expected POOL[=MODEL][:WEIGHT]", s)
	}
	if named && model == "" {
		return nil, fmt.Errorf("target %q names an empty model, drop the = to keep the pool's model", s)
	}
	t.PoolId, t.Model = pool, model
	return t, nil
}

// Whether s is one or more ASCII digits
func digits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// Replaces the rule taking the same match, or appends the rule when none does
func upsertRule(rules []*v1.RewriteRule, rule *v1.RewriteRule) []*v1.RewriteRule {
	out := slices.Clone(rules)
	for i, r := range out {
		if r.GetMatch() == rule.GetMatch() {
			out[i] = rule
			return out
		}
	}
	return append(out, rule)
}

// Copies the limits whose flags were given from src into dst
func mergePolicy(set map[string]bool, dst, src *v1.Policy) {
	if set["max-in-flight"] {
		dst.MaxInFlight = src.GetMaxInFlight()
	}
	if set["rps"] {
		dst.RequestsPerSecond = src.GetRequestsPerSecond()
	}
	if set["burst"] {
		dst.Burst = src.GetBurst()
	}
	if set["timeout"] {
		dst.RequestTimeoutMs = src.GetRequestTimeoutMs()
	}
	if set["upstream-timeout"] {
		dst.UpstreamTimeoutMs = src.GetUpstreamTimeoutMs()
	}
}

// Copies the profile settings whose flags were given from src into dst
func mergeProfile(set map[string]bool, dst, src *v1.Profile) {
	if set["tool-server"] {
		dst.ToolServers = src.GetToolServers()
	}
	if set["mutating-tools"] {
		dst.MutatingTools = src.GetMutatingTools()
	}
}

func runRoutesDelete(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("routes delete"), args, 1, 1, "routes delete <name>")
	if err != nil {
		return err
	}
	resp, err := e.cl.gateway.DeleteRoute(ctx, connect.NewRequest(&v1.DeleteRouteRequest{Name: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "deleted %s\n", resp.Msg.GetRoute().GetName()) })
}

func runObjectivesList(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("objectives list"), args, 0, 0, "objectives list"); err != nil {
		return err
	}
	resp, err := e.cl.gateway.ListObjectives(ctx, connect.NewRequest(&v1.ListObjectivesRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { objectivesTable(w, resp.Msg.GetObjectives()) })
}

func objectivesTable(w io.Writer, list []*v1.Objective) {
	var rows [][]string
	for _, o := range list {
		rows = append(rows, []string{o.GetName(), strconv.Itoa(int(o.GetPriority())), millisText(o.GetTtftMs()), millisText(o.GetTpotMs()), when(o.GetMeta().GetUpdatedAt(), time.RFC3339)})
	}
	table(w, []string{"NAME", "PRIORITY", "TTFT", "TPOT", "UPDATED"}, rows)
}

// Milliseconds with their unit, a dash for zero
func millisText(ms uint32) string {
	if ms == 0 {
		return "-"
	}
	return strconv.Itoa(int(ms)) + "ms"
}

func runObjectivesPut(ctx context.Context, e *env, args []string) error {
	fs := e.flags("objectives put")
	var priority i32
	var ttft, tpot u32
	fs.Var(&priority, "priority", "`band` from -1000 to 1000, higher shed later under saturation, 0 the default band")
	fs.Var(&ttft, "ttft-ms", "time to first token requests are routed to meet, in `milliseconds`, none when 0")
	fs.Var(&tpot, "tpot-ms", "time per output token requests are routed to meet, in `milliseconds`, none when 0")
	positional, err := e.parse(fs, args, 1, 1, "objectives put <name> [--priority N] [--ttft-ms N] [--tpot-ms N]")
	if err != nil {
		return err
	}
	o := &v1.Objective{Name: positional[0], Priority: int32(priority), TtftMs: uint32(ttft), TpotMs: uint32(tpot)}
	resp, err := e.cl.gateway.PutObjective(ctx, connect.NewRequest(&v1.PutObjectiveRequest{Objective: o}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { objectivesTable(w, []*v1.Objective{resp.Msg.GetObjective()}) })
}

func runObjectivesDelete(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("objectives delete"), args, 1, 1, "objectives delete <name>")
	if err != nil {
		return err
	}
	resp, err := e.cl.gateway.DeleteObjective(ctx, connect.NewRequest(&v1.DeleteObjectiveRequest{Name: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "deleted %s\n", resp.Msg.GetObjective().GetName()) })
}

func runSchedulersList(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("schedulers list"), args, 0, 0, "schedulers list"); err != nil {
		return err
	}
	resp, err := e.cl.gateway.ListSchedulerConfigs(ctx, connect.NewRequest(&v1.ListSchedulerConfigsRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, c := range resp.Msg.GetConfigs() {
			accepted, why := "-", ""
			if cond := conditionOf(c.GetStatus().GetConditions(), pools.ConditionAccepted); cond != nil {
				accepted, why = loud(cond.GetStatus()), cond.GetMessage()
			}
			rows = append(rows, []string{c.GetName(), accepted, when(c.GetMeta().GetUpdatedAt(), time.RFC3339), why})
		}
		table(w, []string{"NAME", "ACCEPTED", "UPDATED", "MESSAGE"}, rows)
	})
}

func runSchedulersShow(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("schedulers show"), args, 1, 1, "schedulers show <name>")
	if err != nil {
		return err
	}
	resp, err := e.cl.gateway.ListSchedulerConfigs(ctx, connect.NewRequest(&v1.ListSchedulerConfigsRequest{}))
	if err != nil {
		return err
	}
	for _, c := range resp.Msg.GetConfigs() {
		if c.GetName() == positional[0] {
			return e.print(c, func(w io.Writer) { renderScheduler(w, c) })
		}
	}
	return fmt.Errorf("no scheduler configuration named %s, see nebu schedulers list", positional[0])
}

// Prints a scheduler configuration's conditions then its YAML
func renderScheduler(w io.Writer, c *v1.SchedulerConfig) {
	fmt.Fprintf(w, "%s updated %s\n", c.GetName(), when(c.GetMeta().GetUpdatedAt(), time.RFC3339))
	section(w, "conditions")
	conditionsTable(w, c.GetStatus().GetConditions())
	section(w, "yaml")
	fmt.Fprintln(w, strings.TrimRight(c.GetSpec().GetYaml(), "\n"))
}

// Reads a scheduler configuration's YAML from the file the flag names
func schedulerFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("--file names the YAML configuration and is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func runSchedulersPut(ctx context.Context, e *env, args []string) error {
	fs := e.flags("schedulers put")
	file := fs.String("file", "", "the YAML configuration with its plugins, profiles, and profile handler")
	positional, err := e.parse(fs, args, 1, 1, "schedulers put <name> --file PATH")
	if err != nil {
		return err
	}
	data, err := schedulerFile(*file)
	if err != nil {
		return err
	}
	resp, err := e.cl.gateway.PutSchedulerConfig(ctx, connect.NewRequest(&v1.PutSchedulerConfigRequest{Config: &v1.SchedulerConfig{Name: positional[0], Spec: &v1.SchedulerConfigSpec{Yaml: data}}}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { renderScheduler(w, resp.Msg.GetConfig()) })
}

func runSchedulersValidate(ctx context.Context, e *env, args []string) error {
	fs := e.flags("schedulers validate")
	file := fs.String("file", "", "the YAML configuration to load")
	positional, err := e.parse(fs, args, 1, 1, "schedulers validate <name> --file PATH")
	if err != nil {
		return err
	}
	data, err := schedulerFile(*file)
	if err != nil {
		return err
	}
	resp, err := e.cl.gateway.PutSchedulerConfig(ctx, connect.NewRequest(&v1.PutSchedulerConfigRequest{Config: &v1.SchedulerConfig{Name: positional[0], Spec: &v1.SchedulerConfigSpec{Yaml: data}}, ValidateOnly: true}))
	if err != nil {
		return err
	}
	cond := conditionOf(resp.Msg.GetConfig().GetStatus().GetConditions(), pools.ConditionAccepted)
	switch {
	case cond == nil:
		return fmt.Errorf("scheduler configuration %s: the daemon answered without an %s condition", positional[0], pools.ConditionAccepted)
	case cond.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE:
		return fmt.Errorf("scheduler configuration %s is not accepted, %s: %s", positional[0], cond.GetReason(), cond.GetMessage())
	}
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "%s %s %s %s: %s\n", positional[0], cond.GetType(), loud(cond.GetStatus()), cond.GetReason(), cond.GetMessage())
	})
}

func runSchedulersDelete(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("schedulers delete"), args, 1, 1, "schedulers delete <name>")
	if err != nil {
		return err
	}
	resp, err := e.cl.gateway.DeleteSchedulerConfig(ctx, connect.NewRequest(&v1.DeleteSchedulerConfigRequest{Name: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "deleted %s\n", resp.Msg.GetConfig().GetName()) })
}
