package gateway

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	// Relay paths and outcomes
	slotsPath      = "/slots/"
	relayBoth      = "prefill+decode"
	relayDecode    = "decode-only"
	relayNoPrefill = "prefill-failed"
	handoffTimeout = 5 * time.Minute
)

// Returned when every slot of a llama.cpp relay seat has a relay in flight
var errSlotsBusy = errors.New("every relay slot is in use")

// Everything one relay works with, the seats, the rendered request, and what the prefill seat handed off
type relayCall struct {
	g                     *Gateway
	ctx                   context.Context
	cancel                context.CancelFunc
	route                 *v1.Route
	prefill, decode       *v1.RouteSeat
	prefillURL, decodeURL *url.URL
	path                  string
	out                   []byte
	policy                *v1.Policy
	from                  http.Header
	traceID               string
	// The connector parameters the prefill seat answered with, for the NIXL handoff
	kv map[string]any
	// The bootstrap room both seats meet in, for the SGLang handoff
	room uint64
	// The slots held on each seat and the saved file, for the llama.cpp handoff
	prefillSlot, decodeSlot int
	prefillHeld, decodeHeld bool
	file                    string
}

// The moves of a relay handoff, the prefill request, taking its answer, and the decode request carrying it
type relayHandoff interface {
	// Whether both seats take the request at once, the prefill outcome awaited beside the decode answer
	together() bool
	// The prefill seat's request, the prompt for one token with the handoff parameters
	prefill(c *relayCall) ([]byte, error)
	// Takes what the prefill seat answered, moving whatever must reach the decode seat first
	take(ctx context.Context, c *relayCall, answer []byte) error
	// The decode seat's request, carrying what the prefill seat handed off
	decode(c *relayCall) ([]byte, error)
	// Frees what the handoff held on the seats, once the decode seat answered or the relay failed
	release(c *relayCall)
}

// The handoff a runtime declares, or an error naming a runtime that declares none
func handoffFor(runtimeID, kind string) (relayHandoff, error) {
	switch kind {
	case estimate.HandoffNIXL:
		return nixlHandoff{}, nil
	case estimate.HandoffSGLangBootstrap:
		return bootstrapHandoff{}, nil
	case estimate.HandoffLlamaSlot:
		return slotHandoff{}, nil
	case "":
		return nil, fmt.Errorf("runtime %s declares no relay handoff", runtimeID)
	}
	return nil, fmt.Errorf("runtime %s declares relay handoff %q, which the gateway does not drive", runtimeID, kind)
}

// The prefill and decode seats of a relay route
func relaySeats(route *v1.Route) (prefill, decode *v1.RouteSeat) {
	for _, s := range route.GetSeats() {
		switch s.GetRole() {
		case "prefill":
			prefill = s
		case "decode":
			decode = s
		}
	}
	return prefill, decode
}

// Serves a relay, the prefill seat computing the prompt and the decode seat answering with what it handed off
func (x *exchange) serveRelay() {
	g, t, route, name := x.g, x.w.t, x.route, x.name
	prefill, decode := relaySeats(route)
	if decode == nil || decode.GetEndpoint() == "" {
		x.w.Header().Set("Retry-After", retryAfter)
		x.refuse(failure(http.StatusServiceUnavailable, "model "+name+" has no decode seat ready", chat.CodeModelStarting))
		return
	}
	decodeURL, err := url.Parse(decode.GetEndpoint())
	if err != nil {
		x.refuse(failure(http.StatusBadGateway, "the decode seat of "+name+" has an endpoint the gateway cannot parse: "+err.Error(), chat.CodeServer))
		return
	}
	handoff, err := handoffFor(route.GetRuntimeId(), route.GetRelayHandoff())
	if err != nil {
		x.refuse(failure(http.StatusServiceUnavailable, "model "+name+" cannot relay: "+err.Error(), chat.CodeServer))
		return
	}
	decodeTarget := []target{{url: decodeURL, seat: decode.GetInstanceId()}}
	hold := g.holder(route, decodeTarget)
	hold.to(0)
	defer hold.done()
	x.targets, x.hold, x.relay = decodeTarget, hold, true
	x.open = g.relayOpener(t, x.in.from, name, route, x.policy, handoff, prefill, decode, decodeURL, decodeTarget)
	x.serve()
}

// Opens the decode seat's answer to one rendered request, the prefill seat going first when it pays
func (g *Gateway) relayOpener(t *v1.Trace, from http.Header, name string, route *v1.Route, policy *v1.Policy, handoff relayHandoff, prefill, decode *v1.RouteSeat, decodeURL *url.URL, decodeTarget []target) opener {
	return func(ctx context.Context, req *chat.Request, path string, out []byte) (*http.Response, context.CancelFunc, int, error) {
		decodeOnly := func(why string) (*http.Response, context.CancelFunc, int, error) {
			t.Relay, t.Seats = why, []string{decode.GetInstanceId()}
			t.UpstreamRequest = string(out)
			resp, cancel, err := g.send(ctx, decodeURL, path, out, req.Stream, policy, from)
			return resp, cancel, 0, err
		}
		prompt := chat.EstimateTokens(req)
		breakEven := route.GetRelayBreakEvenPrompt()
		if prefill == nil || prefill.GetState() != v1.InstanceState_INSTANCE_STATE_READY || prefill.GetEndpoint() == "" || breakEven == 0 || uint32(prompt) < breakEven {
			return decodeOnly(relayDecode)
		}
		prefillURL, err := url.Parse(prefill.GetEndpoint())
		if err != nil {
			g.log.Warn("relay prefill seat has an endpoint the gateway cannot parse, decoding alone", "model", name, "seat", prefill.GetInstanceId(), "err", err)
			return decodeOnly(relayNoPrefill)
		}
		releasePrefill := g.table.HoldSeat(prefill.GetInstanceId())
		hctx, hcancel := context.WithTimeout(ctx, handoffTimeout)
		c := &relayCall{g: g, ctx: hctx, cancel: hcancel, route: route, prefill: prefill, decode: decode, prefillURL: prefillURL, decodeURL: decodeURL, path: path, out: out, policy: policy, from: from, traceID: t.GetId()}
		free := func() {
			handoff.release(c)
			hcancel()
			releasePrefill()
		}
		prefillBody, err := handoff.prefill(c)
		if err != nil {
			free()
			if errors.Is(err, errSlotsBusy) {
				return nil, nil, -1, err
			}
			g.log.Warn("relay prefill request could not be made, decoding alone", "model", name, "err", err)
			return decodeOnly(relayNoPrefill)
		}
		decodeBody, err := handoff.decode(c)
		if err != nil && handoff.together() {
			free()
			g.log.Warn("relay decode request could not be made, decoding alone", "model", name, "err", err)
			return decodeOnly(relayNoPrefill)
		}
		var resp *http.Response
		var cancel context.CancelFunc
		if handoff.together() {
			var prefillErr error
			resp, cancel, prefillErr, err = g.relayTogether(ctx, req, name, c, handoff, prefillBody, decodeBody, decodeTarget)
			if err != nil {
				free()
				return nil, nil, -1, err
			}
			if prefillErr != nil {
				free()
				g.log.Warn("relay prefill failed, decoding alone", "model", name, "seat", prefillURL.Host, "err", prefillErr)
				return decodeOnly(relayNoPrefill)
			}
		} else {
			answer, err := g.sendJSON(hctx, prefillURL, path, prefillBody, policy, from)
			if err == nil {
				err = handoff.take(hctx, c, answer)
			}
			if err != nil {
				free()
				g.log.Warn("relay prefill failed, decoding alone", "model", name, "seat", prefillURL.Host, "err", err)
				return decodeOnly(relayNoPrefill)
			}
			if decodeBody, err = handoff.decode(c); err != nil {
				free()
				g.log.Warn("relay decode request could not be made, decoding alone", "model", name, "err", err)
				return decodeOnly(relayNoPrefill)
			}
			if resp, cancel, _, err = g.open(ctx, decodeTarget, nil, path, decodeBody, req.Stream, policy, from, name); err != nil {
				free()
				return nil, nil, -1, err
			}
		}
		t.Relay, t.Seats = relayBoth, []string{prefill.GetInstanceId(), decode.GetInstanceId()}
		t.UpstreamRequest = string(decodeBody)
		return resp, func() {
			cancel()
			free()
		}, 0, nil
	}
}

// Sends the request to both seats at once, answering the prefill failure apart so the caller decodes alone
func (g *Gateway) relayTogether(ctx context.Context, req *chat.Request, name string, c *relayCall, handoff relayHandoff, prefillBody, decodeBody []byte, decodeTarget []target) (*http.Response, context.CancelFunc, error, error) {
	prefilled := make(chan error, 1)
	go func() {
		answer, err := g.sendJSON(c.ctx, c.prefillURL, c.path, prefillBody, c.policy, c.from)
		if err == nil {
			err = handoff.take(c.ctx, c, answer)
		}
		prefilled <- err
	}()
	type opened struct {
		resp   *http.Response
		cancel context.CancelFunc
		err    error
	}
	dctx, dcancel := context.WithCancel(ctx)
	openCh := make(chan opened, 1)
	go func() {
		resp, cancel, _, err := g.open(dctx, decodeTarget, nil, c.path, decodeBody, req.Stream, c.policy, c.from, name)
		openCh <- opened{resp: resp, cancel: cancel, err: err}
	}()
	drop := func(o opened) {
		if o.resp != nil {
			o.resp.Body.Close()
			o.cancel()
		}
	}
	var o opened
	var prefillErr error
	select {
	case prefillErr = <-prefilled:
		if prefillErr != nil {
			dcancel()
		}
		o = <-openCh
	case o = <-openCh:
		if o.err != nil {
			// The decode seat never joined, so the prefill seat has no one to hand off to
			c.cancel()
			<-prefilled
			dcancel()
			return nil, nil, nil, o.err
		}
		prefillErr = <-prefilled
	}
	if prefillErr != nil {
		drop(o)
		dcancel()
		return nil, nil, prefillErr, nil
	}
	if o.err != nil {
		dcancel()
		return nil, nil, nil, o.err
	}
	return o.resp, func() {
		o.cancel()
		dcancel()
	}, nil, nil
}

// Adds fields to a JSON object body
func withFields(body []byte, fields map[string]any) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("request is not a JSON object: %w", err)
	}
	for k, v := range fields {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		obj[k] = raw
	}
	return json.Marshal(obj)
}

// Sends a JSON body and reads the whole answer
func (g *Gateway) sendJSON(ctx context.Context, target *url.URL, path string, body []byte, policy *v1.Policy, from http.Header) ([]byte, error) {
	resp, cancel, err := g.send(ctx, target, path, body, false, policy, from)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%s answered %d: %s", target.Host, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

// The vLLM handoff, the prefill seat answering with connector parameters the decode request carries
type nixlHandoff struct{}

func (nixlHandoff) together() bool { return false }

func (nixlHandoff) prefill(c *relayCall) ([]byte, error) {
	return withFields(c.out, map[string]any{
		"max_tokens": 1, "max_completion_tokens": 1, "stream": false,
		"kv_transfer_params": map[string]any{"do_remote_decode": true, "do_remote_prefill": false, "remote_engine_id": nil, "remote_block_ids": nil, "remote_host": nil, "remote_port": nil},
	})
}

func (nixlHandoff) take(_ context.Context, c *relayCall, answer []byte) error {
	var resp struct {
		KV json.RawMessage `json:"kv_transfer_params"`
	}
	if err := json.Unmarshal(answer, &resp); err != nil || len(resp.KV) == 0 || string(resp.KV) == "null" {
		return errors.New("the prefill seat answered without kv_transfer_params")
	}
	var params map[string]any
	if err := json.Unmarshal(resp.KV, &params); err != nil {
		return fmt.Errorf("kv_transfer_params: %w", err)
	}
	params["do_remote_prefill"] = true
	params["do_remote_decode"] = false
	c.kv = params
	return nil
}

func (nixlHandoff) decode(c *relayCall) ([]byte, error) {
	if c.kv == nil {
		return nil, errors.New("the prefill seat handed off no kv_transfer_params")
	}
	return withFields(c.out, map[string]any{"kv_transfer_params": c.kv})
}

func (nixlHandoff) release(*relayCall) {}

// The SGLang handoff, both seats taking the request at once with the prefill seat's bootstrap address and room
type bootstrapHandoff struct{}

func (bootstrapHandoff) together() bool { return true }

// The bootstrap fields both seats carry
func (bootstrapHandoff) fields(c *relayCall) map[string]any {
	return map[string]any{"bootstrap_host": c.prefill.GetAddress(), "bootstrap_port": c.prefill.GetAuxPort(), "bootstrap_room": c.room}
}

func (h bootstrapHandoff) prefill(c *relayCall) ([]byte, error) {
	if c.prefill.GetAuxPort() == 0 || c.prefill.GetAddress() == "" {
		return nil, errors.New("the prefill seat has no bootstrap address")
	}
	var roomBytes [8]byte
	if _, err := rand.Read(roomBytes[:]); err != nil {
		return nil, err
	}
	c.room = binary.LittleEndian.Uint64(roomBytes[:]) >> 1
	fields := h.fields(c)
	fields["stream"], fields["max_tokens"], fields["max_completion_tokens"] = false, 1, 1
	return withFields(c.out, fields)
}

func (bootstrapHandoff) take(context.Context, *relayCall, []byte) error { return nil }

func (h bootstrapHandoff) decode(c *relayCall) ([]byte, error) {
	return withFields(c.out, h.fields(c))
}

func (bootstrapHandoff) release(*relayCall) {}

// The llama.cpp handoff, a saved prefill slot moved over the mesh and restored into a decode slot
type slotHandoff struct{}

func (slotHandoff) together() bool { return false }

func (slotHandoff) prefill(c *relayCall) ([]byte, error) {
	if c.g.peers == nil {
		return nil, errors.New("this node belongs to no mesh, so no slot file can move between seats")
	}
	ps, ok := c.g.slots.take(c.prefill.GetInstanceId(), int(c.prefill.GetSlots()))
	if !ok {
		return nil, fmt.Errorf("%w on the prefill seat", errSlotsBusy)
	}
	c.prefillSlot, c.prefillHeld = ps, true
	ds, ok := c.g.slots.take(c.decode.GetInstanceId(), int(c.decode.GetSlots()))
	if !ok {
		return nil, fmt.Errorf("%w on the decode seat", errSlotsBusy)
	}
	c.decodeSlot, c.decodeHeld = ds, true
	c.file = "relay-" + c.traceID + ".bin"
	return withFields(c.out, map[string]any{"max_tokens": 1, "max_completion_tokens": 1, "stream": false, "id_slot": ps, "cache_prompt": true})
}

func (slotHandoff) take(ctx context.Context, c *relayCall, _ []byte) error {
	save, err := json.Marshal(map[string]string{"filename": c.file})
	if err != nil {
		return err
	}
	if _, err := c.g.sendJSON(ctx, c.prefillURL, slotsPath+strconv.Itoa(c.prefillSlot)+"?action=save", save, c.policy, nil); err != nil {
		return fmt.Errorf("slot save: %w", err)
	}
	// The prefill slot is free once its state is on disk
	c.g.slots.give(c.prefill.GetInstanceId(), c.prefillSlot)
	c.prefillHeld = false
	formation, prefillNode, decodeNode := c.route.GetFormationId(), c.prefill.GetNodeId(), c.decode.GetNodeId()
	moved := c.g.peers.MoveSlotTo(ctx, formation, decodeNode, prefillNode, c.file)
	if prefillNode != decodeNode {
		c.g.dropSlot(prefillNode, formation, c.file)
	}
	if moved != nil {
		if prefillNode == decodeNode {
			c.g.dropSlot(decodeNode, formation, c.file)
		}
		return fmt.Errorf("slot move: %w", moved)
	}
	_, restored := c.g.sendJSON(ctx, c.decodeURL, slotsPath+strconv.Itoa(c.decodeSlot)+"?action=restore", save, c.policy, nil)
	c.g.dropSlot(decodeNode, formation, c.file)
	if restored != nil {
		return fmt.Errorf("slot restore: %w", restored)
	}
	return nil
}

func (slotHandoff) decode(c *relayCall) ([]byte, error) {
	return withFields(c.out, map[string]any{"id_slot": c.decodeSlot, "cache_prompt": true})
}

func (slotHandoff) release(c *relayCall) {
	if c.prefillHeld {
		c.g.slots.give(c.prefill.GetInstanceId(), c.prefillSlot)
		c.prefillHeld = false
	}
	if c.decodeHeld {
		c.g.slots.give(c.decode.GetInstanceId(), c.decodeSlot)
		c.decodeHeld = false
	}
}

// Removes a relay's slot file from a node on its own deadline, so an answered request is not held up
func (g *Gateway) dropSlot(nodeID, formationID, file string) {
	ctx, cancel := context.WithTimeout(context.Background(), handoffTimeout)
	defer cancel()
	if err := g.peers.DropSlotOn(ctx, nodeID, formationID, file); err != nil {
		g.log.Warn("relay slot file not removed", "node", nodeID, "formation", formationID, "file", file, "err", err)
	}
}

// Sequence slots in use on each llama.cpp relay seat, one relay in flight per slot
type slotPool struct {
	mu   sync.Mutex
	used map[string]map[int]bool
}

func newSlotPool() *slotPool { return &slotPool{used: map[string]map[int]bool{}} }

// Takes a free slot of the seat's n, false when every one is in use
func (p *slotPool) take(seat string, n int) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	held, ok := p.used[seat]
	if !ok {
		held = map[int]bool{}
		p.used[seat] = held
	}
	for id := range max(n, 1) {
		if !held[id] {
			held[id] = true
			return id, true
		}
	}
	return 0, false
}

// Returns a slot to the seat
func (p *slotPool) give(seat string, id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if held, ok := p.used[seat]; ok {
		delete(held, id)
		if len(held) == 0 {
			delete(p.used, seat)
		}
	}
}
