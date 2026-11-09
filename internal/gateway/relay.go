package gateway

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

// Relay outcomes a trace records and the label an SGLang prefill endpoint carries its bootstrap port under
const (
	relayBoth          = requestcontrol.RelayBoth
	relayNoPrefill     = requestcontrol.RelayNoPrefill
	relayWarm          = requestcontrol.RelayWarm
	LabelBootstrapPort = requestcontrol.LabelBootstrapPort
)

// The endpoints of the profile the decision ran beside the primary, the prefill seats of a disaggregated pool
func (x *exchange) prefill() []datalayer.Endpoint {
	if x.dec == nil || x.dec.Result == nil {
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(x.dec.Result.ProfileResults)) {
		if r := x.dec.Result.ProfileResults[name]; name != x.dec.Result.PrimaryProfile && r != nil && len(r.Endpoints) > 0 {
			return r.Endpoints
		}
	}
	return nil
}

// Sends the first round to the decode seats through the handoff plugin the decision names
func (x *exchange) relay(ctx context.Context, out *upstream, prefill []datalayer.Endpoint) (*http.Response, datalayer.Endpoint, error) {
	decode := x.dec.Endpoints
	x.t.Seats = []string{prefill[0].Labels()[datalayer.LabelInstance], decode[0].Labels()[datalayer.LabelInstance]}
	h, err := x.handoff()
	if err != nil {
		return nil, nil, err
	}
	return h.Relay(ctx, &relayed{x: x, out: out}, prefill, decode)
}

// The handoff the scheduler profile names, the one the pool's runtime declares when the profile names none, refusing a disagreement
func (x *exchange) handoff() (requestcontrol.Handoff, error) {
	unavailable := func(format string, args ...any) error {
		return &chat.Failure{Status: http.StatusServiceUnavailable, Code: chat.CodeModelUnavailable, Message: fmt.Sprintf(format, args...), RetryAfter: x.g.cfg.Retry.Unavailable}
	}
	var b *built
	if st := x.dec.Result.State; st != nil {
		b, _ = framework.ReadState[*built](st, builtKey)
	}
	if b == nil {
		return nil, unavailable("pool %s scheduled prefill seats without a hosted scheduler build naming its handoff", x.pool.ID)
	}
	named, config := b.handoff, b.name
	declared := x.g.runtimes.Handoff(x.pool.Runtime)
	switch {
	case named != "" && declared != "" && named != declared:
		return nil, unavailable("pool %s runs %s, whose seats hand off over %q, but scheduler config %s names handoff %q", x.pool.ID, x.pool.Runtime, declared, config, named)
	case named == "" && declared == "":
		return nil, unavailable("pool %s scheduled prefill seats but neither scheduler config %s nor runtime %s names a relay handoff", x.pool.ID, config, x.pool.Runtime)
	}
	name := cmp.Or(named, declared)
	h, ok := x.g.handoffs[name]
	if !ok {
		return nil, unavailable("pool %s runs %s, which declares relay handoff %q, and the gateway drives only %v", x.pool.ID, x.pool.Runtime, name, requestcontrol.HandoffTypes())
	}
	return h, nil
}

// One request's view of the gateway a handoff plugin drives
type relayed struct {
	x   *exchange
	out *upstream
}

func (r *relayed) Path() string { return r.out.path }

func (r *relayed) Body() []byte { return r.out.body }

// The request as sent with another body, its parsed document dropped when the body changed
func (r *relayed) with(body []byte, stream bool) *upstream {
	out := *r.out
	out.stream = stream
	if !sameBytes(body, r.out.body) {
		out.body, out.doc = body, nil
	}
	return &out
}

func (r *relayed) Whole(ctx context.Context, body []byte, eps []datalayer.Endpoint) ([]byte, error) {
	resp, _, err := r.x.attempt(ctx, r.with(body, false), eps)
	if err != nil {
		return nil, err
	}
	return wholeAnswer(resp, "prefill")
}

func (r *relayed) Beside(ctx context.Context, body []byte, ep datalayer.Endpoint) ([]byte, error) {
	resp, _, err := r.x.send(ctx, ep, r.with(body, false), 0)
	if err != nil {
		return nil, err
	}
	return wholeAnswer(resp, ep.Address().String())
}

func (r *relayed) Send(ctx context.Context, body []byte, eps []datalayer.Endpoint) (*http.Response, datalayer.Endpoint, error) {
	return r.x.attempt(ctx, r.with(body, r.out.stream), eps)
}

func (r *relayed) Relayed(outcome string, sent []byte) {
	r.x.t.Relay = outcome
	if sent != nil {
		r.x.sent = sent
	}
}

func (r *relayed) PrefillFailed(why error) error {
	x := r.x
	x.t.Relay = relayNoPrefill
	x.g.log.Warn("relay prefill failed, refusing the request", "model", x.model, "err", why)
	return &chat.Failure{Status: http.StatusBadGateway, Code: chat.CodeUpstream, Message: fmt.Sprintf("the prefill seat of %s did not hand the request off: %v", x.model, why), RetryAfter: x.g.cfg.Retry.Unavailable}
}

func (r *relayed) TraceID() string { return r.x.t.GetId() }

func (r *relayed) Post(ctx context.Context, ep datalayer.Endpoint, path string, body []byte) error {
	return r.x.g.post(ctx, ep, path, body)
}

func (r *relayed) SlotFiles() requestcontrol.SlotFiles {
	if r.x.g.peers == nil {
		return nil
	}
	return r.x.g.peers
}

func (r *relayed) Warn(msg string, args ...any) { r.x.g.log.Warn(msg, args...) }

// Reads a whole JSON answer, refusing any status past 2xx in the name of who answered
func wholeAnswer(resp *http.Response, who string) ([]byte, error) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%s answered %d: %s", who, resp.StatusCode, raw)
	}
	return raw, nil
}

// Posts a JSON body to an endpoint over HTTP, refusing any status past 2xx
func (g *Gateway) post(ctx context.Context, ep datalayer.Endpoint, path string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+ep.Address().String()+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.transport(datalayer.AppProtocolHTTP, 0, idleUncapped).RoundTrip(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%s answered %d: %s", ep.Address(), resp.StatusCode, raw)
	}
	return nil
}

// Moves a relay's saved slot file to a node from another, for the mesh's slot service
func (g *Gateway) MoveSlot(ctx context.Context, formationID, toNode, fromNode, file string) error {
	if g.peers == nil {
		return errors.New("this node belongs to no mesh")
	}
	return g.peers.MoveSlotTo(ctx, formationID, toNode, fromNode, file)
}

// Removes a relay's slot file from a node, for the mesh's slot service
func (g *Gateway) DropSlot(ctx context.Context, nodeID, formationID, file string) error {
	if g.peers == nil {
		return errors.New("this node belongs to no mesh")
	}
	return g.peers.DropSlotOn(ctx, nodeID, formationID, file)
}
