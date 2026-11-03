package bench

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

// Which traces Record reads
type RecordOptions struct {
	// One route's traces, every route's when empty
	Route string
	// Most recent traces read from the ring and the store, at least one
	Limit int
	// Traces started at or after this time, every listed one when zero
	Since time.Time
}

// What Record read and wrote
type RecordSummary struct {
	Listed   int
	Recorded int
	// Listed traces of kinds that are not generation requests, by kind name
	Excluded map[string]int
}

// Endpoint label holding a name an instance serves its base model under, as internal/instances sets it
const servedLabel = "served"

// Names whose requests the gateway treats as the base model of each pool, by pool id
type baseNames map[string]map[string]bool

// Reads gateway traces through the API and writes the chat and generate ones as JSONL requests in arrival order
func Record(ctx context.Context, client nebuv1connect.GatewayServiceClient, out io.Writer, opts RecordOptions) (RecordSummary, error) {
	sum := RecordSummary{Excluded: map[string]int{}}
	if opts.Limit < 1 {
		return sum, errors.New("record needs a limit of at least one trace")
	}
	poolsResp, err := client.ListPools(ctx, connect.NewRequest(&v1.ListPoolsRequest{}))
	if err != nil {
		return sum, fmt.Errorf("listing pools: %w", err)
	}
	pools := map[string]*v1.Pool{}
	for _, p := range poolsResp.Msg.GetPools() {
		pools[p.GetId()] = p
	}
	routesResp, err := client.ListRoutes(ctx, connect.NewRequest(&v1.ListRoutesRequest{}))
	if err != nil {
		return sum, fmt.Errorf("listing routes: %w", err)
	}
	bases := baseNamesOf(routesResp.Msg.GetRoutes())
	list, err := client.ListTraces(ctx, connect.NewRequest(&v1.ListTracesRequest{Route: opts.Route, Limit: uint32(min(opts.Limit, 1<<31))}))
	if err != nil {
		return sum, fmt.Errorf("listing traces: %w", err)
	}
	var keep []*v1.Trace
	for _, t := range list.Msg.GetTraces() {
		if !opts.Since.IsZero() && t.GetStartedAt().AsTime().Before(opts.Since) {
			continue
		}
		sum.Listed++
		switch t.GetKind() {
		case v1.TraceKind_TRACE_KIND_CHAT, v1.TraceKind_TRACE_KIND_GENERATE:
			keep = append(keep, t)
		default:
			sum.Excluded[strings.ToLower(strings.TrimPrefix(t.GetKind().String(), "TRACE_KIND_"))]++
		}
	}
	slices.SortStableFunc(keep, func(a, b *v1.Trace) int {
		return a.GetStartedAt().AsTime().Compare(b.GetStartedAt().AsTime())
	})
	reqs := make([]Request, 0, len(keep))
	for _, s := range keep {
		full, err := client.GetTrace(ctx, connect.NewRequest(&v1.GetTraceRequest{Id: s.GetId()}))
		if err != nil {
			return sum, fmt.Errorf("fetching trace %s: %w", s.GetId(), err)
		}
		r, err := fromTrace(full.Msg.GetTrace(), keep[0].GetStartedAt().AsTime(), pools, bases)
		if err != nil {
			return sum, fmt.Errorf("trace %s: %w", s.GetId(), err)
		}
		reqs = append(reqs, r)
	}
	if err := Write(out, reqs); err != nil {
		return sum, err
	}
	sum.Recorded = len(reqs)
	return sum, nil
}

// Names each pool serves as its base model, from route targets, owner route matches and routes to its own model
func baseNamesOf(routes []*v1.Route) baseNames {
	out := baseNames{}
	add := func(pool, name string) {
		if name == "" {
			return
		}
		if out[pool] == nil {
			out[pool] = map[string]bool{}
		}
		out[pool][name] = true
	}
	for _, rt := range routes {
		for _, rule := range rt.GetSpec().GetRules() {
			for _, tgt := range rule.GetTargets() {
				add(tgt.GetPoolId(), tgt.GetModel())
				if rt.GetOwnerKind() != "" || tgt.GetModel() == "" {
					add(tgt.GetPoolId(), rule.GetMatch())
				}
				if tgt.GetModel() == "" {
					add(tgt.GetPoolId(), rt.GetName())
				}
			}
		}
	}
	return out
}

// Maps one trace with its bodies to a request at its offset from the first trace
func fromTrace(t *v1.Trace, first time.Time, pools map[string]*v1.Pool, bases baseNames) (Request, error) {
	r := Request{
		OffsetMS:     float64(t.GetStartedAt().AsTime().Sub(first)) / float64(time.Millisecond),
		Model:        t.GetRoute(),
		PromptTokens: int(t.GetPromptTokens()),
		Stream:       t.GetStream(),
		Objective:    t.GetObjective(),
		FairnessID:   t.GetFairnessId(),
	}
	if r.Objective == "" && t.GetPriority() != 0 {
		r.Objective = strconv.Itoa(int(t.GetPriority()))
	}
	parsed, perr := chat.ClientOf(t.GetClientApi()).Parse(t.GetPath(), []byte(t.GetRequest()))
	limit := 0
	switch {
	case perr == nil:
		r.Messages, limit = messagesOf(parsed.Messages), parsed.MaxTokens
		r.Model = cmp.Or(r.Model, parsed.Model)
	case r.PromptTokens == 0:
		return r, fmt.Errorf("request body does not parse and the trace counted no prompt tokens: %w", perr)
	}
	r.MaxTokens = cmp.Or(int(t.GetCompletionTokens()), limit)
	adapter, err := adapterOf(t.GetPoolId(), []string{modelOf(t.GetUpstreamRequest()), modelOf(t.GetRequest()), r.Model}, pools, bases)
	if err != nil {
		return r, err
	}
	r.Adapter = adapter
	if len(r.Messages) == 0 && r.PromptTokens == 0 {
		return r, errors.New("the request carries no prompt")
	}
	return r, nil
}

// The top level model field of a JSON body, empty when absent or unparsable
func modelOf(body string) string {
	var v struct {
		Model string `json:"model"`
	}
	if json.Unmarshal([]byte(body), &v) != nil {
		return ""
	}
	return v.Model
}

// First name the pool runs as an adapter, else the asked name unless it is a base name
func adapterOf(poolID string, names []string, pools map[string]*v1.Pool, bases baseNames) (string, error) {
	names = slices.DeleteFunc(slices.Clone(names), func(n string) bool { return n == "" })
	if poolID == "" || len(names) == 0 {
		return "", nil
	}
	p, ok := pools[poolID]
	if !ok {
		return "", fmt.Errorf("pool %s no longer exists, so its adapters cannot be told from base model names, record since a time after its last trace or a route whose pool exists", poolID)
	}
	for _, name := range names {
		for _, ep := range p.GetStatus().GetEndpoints() {
			m := ep.GetMetrics()
			if ep.GetLabels()[datalayer.LabelAdapter] == name || slices.Contains(m.GetRunningAdapters(), name) || slices.Contains(m.GetWaitingAdapters(), name) {
				return name, nil
			}
		}
	}
	asked := names[0]
	if asked == p.GetSpec().GetModel() || bases[poolID][asked] {
		return "", nil
	}
	for _, ep := range p.GetStatus().GetEndpoints() {
		if ep.GetLabels()[servedLabel] == asked {
			return "", nil
		}
	}
	return asked, nil
}

// Turns parsed messages into text and image messages in the three roles every chat server takes
func messagesOf(in []chat.Message) []Message {
	out := make([]Message, 0, len(in))
	for _, m := range in {
		var parts, images []string
		for _, p := range m.Parts {
			switch p.Type {
			case "image":
				images = append(images, cmp.Or(p.URL, "data:"+p.MediaType+";base64,"+p.Data))
			default:
				parts = append(parts, partText(p))
			}
		}
		for _, c := range m.ToolCalls {
			parts = append(parts, c.Name+"("+c.Args+")")
		}
		role := m.Role
		if role != "system" && role != "assistant" {
			role = "user"
		}
		out = append(out, Message{Role: role, Content: strings.Join(parts, "\n"), Images: images})
	}
	return out
}

// Text of a text, thinking or tokens part, token ids spelled out
func partText(p chat.Part) string {
	if p.Type == "tokens" {
		ids := make([]string, len(p.Tokens))
		for i, id := range p.Tokens {
			ids[i] = strconv.Itoa(id)
		}
		return strings.Join(ids, " ")
	}
	return p.Text
}
