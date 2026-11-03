package requestcontrol

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

// Handoff plugin types, one per protocol carrying a prompt's cache from a prefill seat to the decode seats
const (
	// vLLM's NIXL connector, the prefill answer's kv_transfer_params sent on to decode
	HandoffNIXL = "nixl"
	// SGLang's bootstrap room, both seats taking the request at once with the prefill seat's bootstrap address
	HandoffSGLangBootstrap = "sglang-bootstrap"
	// llama.cpp's saved slot file, decode answering while the prefill seat warms a spare decode slot
	HandoffLlamaSlot = "llamacpp-slot"
)

// Relay outcomes a handoff records on the request's trace
const (
	RelayBoth      = "prefill+decode"
	RelayNoPrefill = "prefill-failed"
	// Decode answered and the slot warm beside it finished, failed, or was still running when the request ended
	RelayWarm        = "decode+slot-warm"
	RelayWarmFailed  = "decode+slot-warm-failed"
	RelayWarmPending = "decode+slot-warm-pending"
)

const (
	// The label a prefill endpoint carries its SGLang bootstrap port under
	LabelBootstrapPort = "bootstrap-port"
	// Time a handoff step may take apart from the request
	HandoffTimeout = 5 * time.Minute
	// Where a llama.cpp server saves and restores slots
	slotsPath = "/slots/"
)

// Carries one request's first round from its prefill seats to its decode seats over one protocol
type Handoff interface {
	framework.Plugin
	// Sends the first round to the decode seats with what the prefill seats handed off, answering decode's response
	Relay(ctx context.Context, r Relay, prefill, decode []datalayer.Endpoint) (*http.Response, datalayer.Endpoint, error)
}

// What the gateway lends a handoff for one request
type Relay interface {
	// The path and body the decision forwards
	Path() string
	Body() []byte
	// Sends a body unstreamed to the endpoints in order under the request's count, refusing any status past 2xx
	Whole(ctx context.Context, body []byte, eps []datalayer.Endpoint) ([]byte, error)
	// Sends a body unstreamed to one endpoint working beside another seat, outside the request's count
	Beside(ctx context.Context, body []byte, ep datalayer.Endpoint) ([]byte, error)
	// Sends a body to the endpoints in order, streamed or whole as the client asked
	Send(ctx context.Context, body []byte, eps []datalayer.Endpoint) (*http.Response, datalayer.Endpoint, error)
	// Records the relay outcome on the trace and the body decode received, a nil body leaving the recorded one
	Relayed(outcome string, sent []byte)
	// The refusal a request answers when its prefill seat did not hand off, recording that outcome
	PrefillFailed(why error) error
	// The id of the request's trace
	TraceID() string
	// Posts a JSON body to an endpoint, refusing any status past 2xx
	Post(ctx context.Context, ep datalayer.Endpoint, path string, body []byte) error
	// Moves slot files between the nodes of a mesh, nil when this node belongs to none
	SlotFiles() SlotFiles
	// Logs a handoff step that failed apart from the request
	Warn(msg string, args ...any)
}

// Moves and removes the slot files a llama.cpp handoff saves, across the nodes of a mesh
type SlotFiles interface {
	MoveSlotTo(ctx context.Context, formationID, toNode, fromNode, file string) error
	DropSlotOn(ctx context.Context, nodeID, formationID, file string) error
}

// Every handoff plugin type with what builds one, each instance holding its own state
var handoffs = map[string]func() Handoff{
	HandoffNIXL:            func() Handoff { return nixl{} },
	HandoffSGLangBootstrap: func() Handoff { return bootstrap{} },
	HandoffLlamaSlot:       func() Handoff { return &slotWarm{slots: newSlotPool()} },
}

// Every handoff plugin type in sorted order
func HandoffTypes() []string {
	out := make([]string, 0, len(handoffs))
	for typ := range handoffs {
		out = append(out, typ)
	}
	slices.Sort(out)
	return out
}

// Builds a handoff plugin of a registered type
func NewHandoff(typ string) (Handoff, error) {
	build, ok := handoffs[typ]
	if !ok {
		return nil, fmt.Errorf("%w %q, known handoffs are %v", ErrUnknownType, typ, HandoffTypes())
	}
	return build(), nil
}

// The body with every top level field set in one pass of the body parser
func withFields(path string, body []byte, fields map[string]any) ([]byte, error) {
	raw := make(map[string][]byte, len(fields))
	for k, v := range fields {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw[k] = b
	}
	doc, err := bodyparse.Parse(bodyparse.KindOf(path), body)
	if err != nil {
		return nil, err
	}
	out, err := doc.WithFields(raw)
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// The vLLM handoff, the prefill seat answering with connector parameters the decode request carries
type nixl struct{}

func (nixl) TypedName() framework.TypedName {
	return framework.TypedName{Type: HandoffNIXL, Name: HandoffNIXL}
}

func (nixl) Relay(ctx context.Context, r Relay, prefill, decode []datalayer.Endpoint) (*http.Response, datalayer.Endpoint, error) {
	pbody, err := withFields(r.Path(), r.Body(), map[string]any{
		"max_tokens": 1, "max_completion_tokens": 1, "stream": false,
		"kv_transfer_params": map[string]any{"do_remote_decode": true, "do_remote_prefill": false, "remote_engine_id": nil, "remote_block_ids": nil, "remote_host": nil, "remote_port": nil},
	})
	if err != nil {
		return nil, nil, err
	}
	answer, err := r.Whole(ctx, pbody, prefill)
	if err != nil {
		return nil, nil, r.PrefillFailed(err)
	}
	var probe struct {
		KV map[string]any `json:"kv_transfer_params"`
	}
	if err := json.Unmarshal(answer, &probe); err != nil || probe.KV == nil {
		return nil, nil, r.PrefillFailed(errors.New("the prefill seat answered without kv_transfer_params"))
	}
	probe.KV["do_remote_prefill"], probe.KV["do_remote_decode"] = true, false
	dbody, err := withFields(r.Path(), r.Body(), map[string]any{"kv_transfer_params": probe.KV})
	if err != nil {
		return nil, nil, err
	}
	r.Relayed(RelayBoth, dbody)
	return r.Send(ctx, dbody, decode)
}

// The SGLang handoff, both seats taking the request at once with the prefill seat's bootstrap address and room
type bootstrap struct{}

func (bootstrap) TypedName() framework.TypedName {
	return framework.TypedName{Type: HandoffSGLangBootstrap, Name: HandoffSGLangBootstrap}
}

func (bootstrap) Relay(ctx context.Context, r Relay, prefill, decode []datalayer.Endpoint) (*http.Response, datalayer.Endpoint, error) {
	seat := prefill[0]
	port, err := strconv.Atoi(seat.Labels()[LabelBootstrapPort])
	if err != nil || port <= 0 {
		return nil, nil, r.PrefillFailed(fmt.Errorf("prefill seat %s carries no %s label", seat.Address(), LabelBootstrapPort))
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], rand.Uint64())
	fields := map[string]any{"bootstrap_host": seat.Address().Host, "bootstrap_port": port, "bootstrap_room": binary.LittleEndian.Uint64(b[:]) >> 1}
	dbody, err := withFields(r.Path(), r.Body(), fields)
	if err != nil {
		return nil, nil, err
	}
	fields["stream"], fields["max_tokens"], fields["max_completion_tokens"] = false, 1, 1
	pbody, err := withFields(r.Path(), r.Body(), fields)
	if err != nil {
		return nil, nil, err
	}
	pctx, pcancel := context.WithTimeout(ctx, HandoffTimeout)
	defer pcancel()
	prefilled := make(chan error, 1)
	go func() {
		_, err := r.Beside(pctx, pbody, seat)
		prefilled <- err
	}()
	dctx, dcancel := context.WithCancel(ctx)
	resp, ep, err := r.Send(dctx, dbody, decode)
	perr := <-prefilled
	if err != nil {
		dcancel()
		return nil, nil, err
	}
	if perr != nil {
		resp.Body.Close()
		dcancel()
		return nil, nil, r.PrefillFailed(perr)
	}
	context.AfterFunc(ctx, dcancel)
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: dcancel}
	r.Relayed(RelayBoth, dbody)
	return resp, ep, nil
}

// Cancels a request's context once its body is closed
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// The llama.cpp handoff, decode answering at once while prefill warms a spare decode slot beside it
type slotWarm struct {
	slots *slotPool
}

func (*slotWarm) TypedName() framework.TypedName {
	return framework.TypedName{Type: HandoffLlamaSlot, Name: HandoffLlamaSlot}
}

func (h *slotWarm) Relay(ctx context.Context, r Relay, prefill, decode []datalayer.Endpoint) (*http.Response, datalayer.Endpoint, error) {
	resp, ep, err := r.Send(ctx, r.Body(), decode)
	if err != nil {
		return nil, nil, err
	}
	r.Relayed(RelayWarmPending, nil)
	done := make(chan error, 1)
	seat := prefill[0]
	go func() { done <- h.warm(r, seat, ep) }()
	resp.Body = &warmBody{ReadCloser: resp.Body, ctx: ctx, done: done, r: r, prefill: seat, decode: ep}
	return resp, ep, nil
}

// Prefills the prompt, saves the slot, moves its file to the decode node and restores it into a spare slot
func (h *slotWarm) warm(r Relay, prefill, decode datalayer.Endpoint) error {
	files := r.SlotFiles()
	if files == nil {
		return errors.New("this node belongs to no mesh, so no slot file can move between seats")
	}
	ctx, cancel := context.WithTimeout(context.Background(), HandoffTimeout)
	defer cancel()
	pseat, dseat := prefill.Labels()[datalayer.LabelInstance], decode.Labels()[datalayer.LabelInstance]
	ps, ok := h.slots.take(pseat, slotsOf(prefill))
	if !ok {
		return fmt.Errorf("%w on the prefill seat", errSlotsBusy)
	}
	defer h.slots.give(pseat, ps)
	ds, ok := h.slots.take(dseat, slotsOf(decode))
	if !ok {
		return fmt.Errorf("%w on the decode seat", errSlotsBusy)
	}
	defer h.slots.give(dseat, ds)
	pbody, err := withFields(r.Path(), r.Body(), map[string]any{"max_tokens": 1, "max_completion_tokens": 1, "stream": false, "id_slot": ps, "cache_prompt": true})
	if err != nil {
		return err
	}
	if err := r.Post(ctx, prefill, r.Path(), pbody); err != nil {
		return fmt.Errorf("prefill: %w", err)
	}
	file := "relay-" + r.TraceID() + ".bin"
	save, err := json.Marshal(map[string]string{"filename": file})
	if err != nil {
		return err
	}
	if err := r.Post(ctx, prefill, slotsPath+strconv.Itoa(ps)+"?action=save", save); err != nil {
		return fmt.Errorf("slot save: %w", err)
	}
	formation, from, to := prefill.Labels()[datalayer.LabelFormation], prefill.Labels()[datalayer.LabelNode], decode.Labels()[datalayer.LabelNode]
	moved := files.MoveSlotTo(ctx, formation, to, from, file)
	if from != to {
		dropSlotFile(r, files, from, formation, file)
	}
	if moved != nil {
		if from == to {
			dropSlotFile(r, files, to, formation, file)
		}
		return fmt.Errorf("slot move: %w", moved)
	}
	restored := r.Post(ctx, decode, slotsPath+strconv.Itoa(ds)+"?action=restore", save)
	dropSlotFile(r, files, to, formation, file)
	if restored != nil {
		return fmt.Errorf("slot restore: %w", restored)
	}
	return nil
}

// Removes a relay's slot file from a node on its own deadline
func dropSlotFile(r Relay, files SlotFiles, nodeID, formationID, file string) {
	ctx, cancel := context.WithTimeout(context.Background(), HandoffTimeout)
	defer cancel()
	if err := files.DropSlotOn(ctx, nodeID, formationID, file); err != nil {
		r.Warn("relay slot file not removed", "node", nodeID, "formation", formationID, "file", file, "err", err)
	}
}

// Decode's answer, whose close waits for the slot warm and records its outcome unless the request ends first
type warmBody struct {
	io.ReadCloser
	ctx             context.Context
	done            chan error
	r               Relay
	prefill, decode datalayer.Endpoint
	once            sync.Once
}

func (b *warmBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() {
		select {
		case werr := <-b.done:
			if werr != nil {
				b.warnFailed(werr)
				b.r.Relayed(RelayWarmFailed, nil)
				return
			}
			b.r.Relayed(RelayWarm, nil)
		case <-b.ctx.Done():
			go func() {
				if werr := <-b.done; werr != nil {
					b.warnFailed(werr)
				}
			}()
		}
	})
	return err
}

func (b *warmBody) warnFailed(err error) {
	b.r.Warn("relay slot warm failed", "prefill", b.prefill.Address().String(), "decode", b.decode.Address().String(), "err", err)
}

// Sequence slots an endpoint serves at once, from its capacity attribute
func slotsOf(ep datalayer.Endpoint) int {
	n, _ := datalayer.Attribute[int](ep, datalayer.CollectorCapacity)
	return max(n, 1)
}

// Returned when every slot of a llama.cpp relay seat has a warm in flight
var errSlotsBusy = errors.New("every relay slot is in use")

// Sequence slots in use on each llama.cpp relay seat, one warm per slot
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
