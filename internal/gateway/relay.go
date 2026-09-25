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

// Everything one relay works with: the seats, the rendered request, and what the prefill seat
// handed off
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

// The three moves of a relay handoff: the prefill seat's request, taking what it answered, and
// the decode seat's request carrying the handoff
type relayHandoff interface {
	// Whether both seats take the request at once, the prefill seat waiting for the decode seat
	// to join, so the prefill's outcome is awaited beside the decode seat's answer
	together() bool
	// The prefill seat's request: the prompt for one token with the handoff parameters
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

// Serves a relay: the prompt goes to the prefill seat for one token with the runtime's handoff,
// then the request goes to the decode seat with what came back and its answer streams. A prompt
// under the plan's break even goes to the decode seat alone, and so does one whose prefill fails.
// The trace names both seats and says which path the request took
func (g *Gateway) relay(w *traceWriter, r *http.Request, body []byte, name string, route *v1.Route, policy *v1.Policy, client Flavor) {
	t := w.t
	prefill, decode := relaySeats(route)
	if decode == nil || decode.GetEndpoint() == "" {
		w.Header().Set("Retry-After", retryAfter)
		g.refuse(w, client, http.StatusServiceUnavailable, "model "+name+" has no decode seat ready", "model_starting")
		return
	}
	decodeURL, err := url.Parse(decode.GetEndpoint())
	if err != nil {
		g.refuse(w, client, http.StatusBadGateway, "the decode seat of "+name+" has an endpoint the gateway cannot parse: "+err.Error(), "server_error")
		return
	}
	handoff, err := handoffFor(route.GetRuntimeId(), route.GetRelayHandoff())
	if err != nil {
		g.refuse(w, client, http.StatusServiceUnavailable, "model "+name+" cannot relay: "+err.Error(), "server_error")
		return
	}
	upstream := flavorOf(route.GetApi())
	chat, err := client.ParseRequest(r.URL.Path, body)
	if err != nil {
		g.refuse(w, client, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	if served := route.GetServed(); served != "" {
		chat.Model = served
	}
	foldSystem(chat, g.table.SystemMode(route))
	t.Translated = clientFlavor(r) != route.GetApi()
	if upstream.InlineImages() {
		if err := g.inlineImages(r.Context(), chat, policy); err != nil {
			g.refuse(w, client, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
	}
	path, out, err := upstream.RenderRequest(chat)
	if err != nil {
		g.refuse(w, client, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	chat.Model = name
	prompt := estimateTokens(chat)
	decodeTarget := []target{{url: decodeURL, seat: decode.GetInstanceId()}}
	hold := g.holder(route, decodeTarget)
	hold.to(0)
	defer hold.done()
	decodeOnly := func(why string) {
		t.Relay, t.Seats = why, []string{decode.GetInstanceId()}
		t.UpstreamRequest = capped(out)
		g.exchange(w, r, chat, name, path, out, decodeTarget, nil, policy, client, upstream)
	}
	breakEven := route.GetRelayBreakEvenPrompt()
	if prefill == nil || prefill.GetState() != v1.InstanceState_INSTANCE_STATE_READY || prefill.GetEndpoint() == "" || breakEven == 0 || uint32(prompt) < breakEven {
		decodeOnly(relayDecode)
		return
	}
	prefillURL, err := url.Parse(prefill.GetEndpoint())
	if err != nil {
		g.log.Warn("relay prefill seat has an endpoint the gateway cannot parse, decoding alone", "model", name, "seat", prefill.GetInstanceId(), "err", err)
		decodeOnly(relayNoPrefill)
		return
	}
	releasePrefill := g.table.HoldSeat(prefill.GetInstanceId())
	defer releasePrefill()
	ctx, cancel := context.WithTimeout(r.Context(), handoffTimeout)
	defer cancel()
	c := &relayCall{g: g, ctx: ctx, cancel: cancel, route: route, prefill: prefill, decode: decode, prefillURL: prefillURL, decodeURL: decodeURL, path: path, out: out, policy: policy, from: r.Header, traceID: t.GetId()}
	defer handoff.release(c)
	prefillBody, err := handoff.prefill(c)
	if err != nil {
		if errors.Is(err, errSlotsBusy) {
			w.Header().Set("Retry-After", retryAfter)
			g.refuse(w, client, http.StatusTooManyRequests, "model "+name+" cannot relay now: "+err.Error()+", retry shortly", "rate_limit_error")
			return
		}
		g.log.Warn("relay prefill request could not be made, decoding alone", "model", name, "err", err)
		decodeOnly(relayNoPrefill)
		return
	}
	if handoff.together() {
		decodeBody, err := handoff.decode(c)
		if err != nil {
			g.log.Warn("relay decode request could not be made, decoding alone", "model", name, "err", err)
			decodeOnly(relayNoPrefill)
			return
		}
		g.relayTogether(w, r, chat, name, c, handoff, prefillBody, decodeBody, decodeTarget, client, upstream, decodeOnly)
		return
	}
	answer, err := g.sendJSON(ctx, prefillURL, path, prefillBody, policy, r.Header)
	if err == nil {
		err = handoff.take(ctx, c, answer)
	}
	if err != nil {
		g.log.Warn("relay prefill failed, decoding alone", "model", name, "seat", prefillURL.Host, "err", err)
		decodeOnly(relayNoPrefill)
		return
	}
	decodeBody, err := handoff.decode(c)
	if err != nil {
		g.log.Warn("relay decode request could not be made, decoding alone", "model", name, "err", err)
		decodeOnly(relayNoPrefill)
		return
	}
	t.Relay, t.Seats = relayBoth, []string{prefill.GetInstanceId(), decode.GetInstanceId()}
	t.UpstreamRequest = capped(decodeBody)
	g.exchange(w, r, chat, name, path, decodeBody, decodeTarget, nil, policy, client, upstream)
}

// Sends the request to both seats at once, the decode seat's answer streaming once the prefill
// seat's outcome is known. A prefill that fails before anything streamed sends the request to
// the decode seat alone
func (g *Gateway) relayTogether(w *traceWriter, r *http.Request, chat *Chat, name string, c *relayCall, handoff relayHandoff, prefillBody, decodeBody []byte, decodeTarget []target, client, upstream Flavor, decodeOnly func(string)) {
	t := w.t
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
	dctx, dcancel := context.WithCancel(r.Context())
	defer dcancel()
	openCh := make(chan opened, 1)
	go func() {
		resp, cancel, _, err := g.open(dctx, decodeTarget, nil, c.path, decodeBody, chat.Stream, c.policy, c.from, name)
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
			// The decode seat never joined, so the prefill seat has no one to hand off to.
			c.cancel()
			<-prefilled
			g.upstreamError(w, t, client, name, o.err)
			return
		}
		prefillErr = <-prefilled
	}
	if prefillErr != nil {
		drop(o)
		g.log.Warn("relay prefill failed, decoding alone", "model", name, "seat", c.prefillURL.Host, "err", prefillErr)
		decodeOnly(relayNoPrefill)
		return
	}
	if o.err != nil {
		g.upstreamError(w, t, client, name, o.err)
		return
	}
	t.Relay, t.Seats = relayBoth, []string{c.prefill.GetInstanceId(), c.decode.GetInstanceId()}
	t.UpstreamRequest = capped(decodeBody)
	g.deliver(w, r, chat, name, o.resp, o.cancel, client, upstream)
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

// The vLLM handoff: the prefill seat computes the prompt for one token and answers with the
// connector's transfer parameters, which the decode request carries so the cache moves to it
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

// The SGLang handoff: the client hands the decode seat the prefill seat's bootstrap address and
// a room id, and both seats take the request at once
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

// The llama.cpp handoff: the prefill seat computes the prompt into a slot and saves it, the file
// moves to the decode seat's node over the mesh blob channel, the decode seat restores it into a
// slot of its own and continues with prompt caching on, and the file leaves both disks
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
	// The prefill slot is free once its state is on disk.
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

// Removes a relay's slot file from a node once it is no longer read, on its own deadline so a
// request already answered is not held up
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
