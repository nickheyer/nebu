package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Rounds of tool calls one request may make before the model has to answer
const maxToolRounds = 32

// Everything one request to a language model works with on its way to an answer
type exchange struct {
	g      *Gateway
	w      *traceWriter
	r      *http.Request
	in     *inbound
	name   string
	api    v1.ApiFlavor
	client chat.Client
	route  *v1.Route
	policy *v1.Policy
	// The seats the request may go to and the count that follows it between them
	targets []target
	hold    *seatHold
	open    opener
	runtime chat.Runtime
	plan    *toolPlan
	// Whether the route is a relay, whose requests are always rendered
	relay bool
}

// Answers an error in the client's protocol and records it on the trace
func (x *exchange) refuse(f chat.Failure) {
	x.g.refuse(x.w, x.client, f.Status, f.Message, f.Code)
}

// Serves the request as the client sent it unless the runtime needs it rendered, answering the seat index used
func (x *exchange) serve() int {
	t, in, route := x.w.t, x.in, x.route
	mode := x.g.table.SystemMode(route)
	same := x.api == route.GetApi()
	if in.req == nil {
		if !same || x.relay {
			x.refuse(chat.FailureOf(in.err))
			return -1
		}
		return x.passthrough()
	}
	req := in.req
	x.runtime = chat.RuntimeOf(route.GetApi())
	x.plan = emptyPlan(x.g)
	if req.Kind == chat.Chat {
		plan, err := x.g.plan(x.r.Context(), req, route, x.r.Header)
		if err != nil {
			x.refuse(chat.FailureOf(err))
			return -1
		}
		x.plan = plan
	}
	defer x.plan.close()
	emulate := x.g.table.ToolSupport(route) == v1.ToolSupport_TOOL_SUPPORT_EMULATED && len(req.Tools) > 0
	if same && !x.plan.active() && !emulate && !foldsSystem(req, mode) && req.Kind != chat.Count && !x.relay {
		return x.passthrough()
	}
	t.Translated = !same
	if served := route.GetServed(); served != "" {
		req.Model = served
	}
	foldSystem(req, mode)
	if x.runtime.Traits().Strict && !same {
		chat.DropExtras(req)
	}
	if req.Kind == chat.Count {
		return x.count()
	}
	if x.runtime.Traits().InlineImages {
		if err := x.g.inlineImages(x.r.Context(), req, x.policy); err != nil {
			x.refuse(failure(http.StatusBadRequest, err.Error(), chat.CodeInvalidRequest))
			return -1
		}
	}
	if emulate {
		x.runtime = chat.Emulate(x.runtime)
	}
	return x.rounds()
}

// Proxies the body as the client sent it, the model renamed to what the runtime serves
func (x *exchange) passthrough() int {
	g, t, r, in := x.g, x.w.t, x.r, x.in
	body := in.body
	if served := x.route.GetServed(); served != "" && served != x.name {
		var err error
		if body, err = renameModel(body, served); err != nil {
			x.refuse(failure(http.StatusBadRequest, err.Error(), chat.CodeInvalidRequest))
			return -1
		}
		t.UpstreamRequest = string(body)
	}
	// The answer is parsed into the trace as it passes, an unparsed one kept whole
	if in.req != nil {
		reader := readPassthrough(t, chat.RuntimeOf(x.route.GetApi()), in.req)
		x.w.tee = reader.pw
		defer reader.close()
	} else {
		raw := &rawCapture{}
		x.w.tee = raw
		defer func() { t.Response = raw.buf.String() }()
	}
	ctx := r.Context()
	if d := x.policy.GetRequestTimeoutMs(); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(d)*time.Millisecond)
		defer cancel()
	}
	first := x.targets[0].url
	hops := &failover{targets: x.targets, hold: x.hold, next: &sameHostRedirects{next: g.transport(x.policy.GetUpstreamTimeoutMs()), body: body}, log: g.log}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(first)
			pr.Out.URL.Path = r.URL.Path
			pr.Out.URL.RawPath = r.URL.RawPath
			pr.Out.Host = first.Host
			dropClientOnly(pr.Out.Header)
		},
		Transport:     hops,
		FlushInterval: -1,
		ErrorHandler:  func(w http.ResponseWriter, _ *http.Request, err error) { g.upstreamError(w, t, x.client, x.name, err) },
	}
	r = r.WithContext(ctx)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	rp.ServeHTTP(x.w, r)
	return hops.used
}

// Counts the prompt's tokens with the runtime's tokenizer, estimating when it has none
func (x *exchange) count() int {
	req := x.in.req
	res := &chat.Result{Model: x.name}
	n, used, err := x.countUpstream()
	if err != nil {
		x.g.log.Debug("gateway count estimated", "model", x.name, "err", err)
		n = chat.EstimateTokens(req)
	} else if !x.runtime.Traits().CountsImages {
		n += chat.ImageTokens(req)
	}
	res.Usage.In = n
	x.w.t.PromptTokens = uint32(n)
	out := x.client.Answer(x.w, req, x.name)
	defer out.Close()
	if err := out.Write(chat.Event{Kind: chat.Stopped, Res: res}); err != nil {
		x.refuse(failure(http.StatusInternalServerError, err.Error(), chat.CodeServer))
	}
	return used
}

// Asks the runtime's tokenizer, answering the count and the seat that gave it
func (x *exchange) countUpstream() (int, int, error) {
	req := x.in.req
	path, out, err := x.runtime.Render(req)
	if err != nil {
		return 0, -1, err
	}
	x.w.t.UpstreamRequest = string(out)
	resp, cancel, used, err := x.open(x.r.Context(), req, path, out)
	if err != nil {
		return 0, used, err
	}
	defer cancel()
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, used, err
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return 0, used, fmt.Errorf("tokenizer answered %d", resp.StatusCode)
	}
	res, err := x.runtime.ParseResult(req, raw)
	if err != nil {
		return 0, used, err
	}
	return res.Usage.In, used, nil
}

// Asks the model round by round, running the tools it calls, until it answers or calls one of the client's
func (x *exchange) rounds() int {
	g, t, r, req, plan := x.g, x.w.t, x.r, x.in.req, x.plan
	sink := &traceSink{t: t}
	defer sink.close()
	var out chat.Writer
	fail := func(f chat.Failure) {
		t.Error = f.Message
		if out == nil {
			x.client.Error(x.w, f)
			return
		}
		out.Write(chat.Event{Kind: chat.Failed, Failure: &f})
	}
	total := &chat.Result{Model: x.name}
	used := -1
	for round := 0; ; round++ {
		path, body, err := x.runtime.Render(req)
		if err != nil {
			fail(chat.FailureOf(err))
			break
		}
		t.UpstreamRequest = string(body)
		resp, cancel, i, err := x.open(r.Context(), req, path, body)
		if err != nil {
			g.log.Warn("gateway round", "model", x.name, "err", err)
			if errors.Is(err, errSlotsBusy) {
				x.w.Header().Set("Retry-After", retryAfter)
			}
			fail(upstreamFailure(x.name, err))
			break
		}
		used = i
		if out == nil {
			if after := resp.Header.Get("Retry-After"); after != "" {
				x.w.Header().Set("Retry-After", after)
			}
		}
		if out == nil && resp.StatusCode < http.StatusMultipleChoices {
			t.FirstByteAt = timestamppb.Now()
			out = x.client.Answer(x.w, req, x.name)
		}
		var said strings.Builder
		res, err := x.round(resp, func(ev chat.Event) error {
			if ev.Kind == chat.Said {
				said.WriteString(ev.Text)
			}
			sink.event(ev)
			return out.Write(ev)
		})
		cancel()
		if err != nil {
			switch {
			case r.Context().Err() != nil:
				t.Stop, t.Error = "cancelled", "client went away"
				g.log.Info("gateway round", "model", x.name, "err", t.Error)
			case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
				fail(failure(http.StatusGatewayTimeout, fmt.Sprintf("model %s did not answer within its timeout", x.name), chat.CodeTimeout))
			default:
				g.log.Warn("gateway round", "model", x.name, "err", err)
				if refusal, ok := errors.AsType[*chat.Failure](err); ok {
					fail(*refusal)
					t.Error = "upstream: " + refusal.Message
				} else {
					fail(failure(http.StatusBadGateway, "upstream: "+err.Error(), chat.CodeServer))
				}
			}
			break
		}
		if total.ID == "" {
			total.ID = res.ID
		}
		total.Usage.In, total.Usage.Out, total.Stop, total.Extra = total.Usage.In+res.Usage.In, total.Usage.Out+res.Usage.Out, res.Stop, mergeExtra(total.Extra, res.Extra)
		total.Usage.DraftOffered, total.Usage.DraftAccepted = total.Usage.DraftOffered+res.Usage.DraftOffered, total.Usage.DraftAccepted+res.Usage.DraftAccepted
		total.Usage.Rounds = append(total.Usage.Rounds, res.Usage.RoundsOrSelf()...)
		if len(res.ToolCalls) > 0 {
			sink.first()
		}
		var runs []*chat.Run
		var held []chat.ToolCall
		for _, c := range res.ToolCalls {
			pl, mine := plan.tools[c.Name]
			if !mine || plan.handsBack(pl) {
				held = append(held, c)
				continue
			}
			run := plan.run(r.Context(), c, round >= maxToolRounds)
			runs = append(runs, run)
			total.Runs = append(total.Runs, run)
			t.ToolCalls = append(t.ToolCalls, traceRun(run))
			if err := out.Write(chat.Event{Kind: chat.Ran, Run: run}); err != nil {
				return used
			}
		}
		if len(runs) == 0 || len(held) > 0 {
			for i, c := range held {
				if err := out.Write(chat.Event{Kind: chat.Called, Index: i, Tool: &chat.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args}}); err != nil {
					return used
				}
			}
			total.ToolCalls = held
			if err := out.Write(chat.Event{Kind: chat.Stopped, Res: total}); err != nil {
				if _, ok := errors.AsType[*chat.ToolArgsError](err); ok {
					fail(failure(http.StatusBadGateway, "upstream: "+err.Error(), chat.CodeUpstream))
				}
			}
			break
		}
		var parts []chat.Part
		if res.Thinking != "" {
			parts = append(parts, chat.Part{Type: "thinking", Text: res.Thinking})
		}
		if said.Len() > 0 {
			parts = append(parts, chat.Part{Type: "text", Text: said.String()})
		}
		req.Messages = append(append(req.Messages, chat.Message{Role: "assistant", Parts: parts, ToolCalls: res.ToolCalls}), toolTurns(runs)...)
		// A forced first call has been made, the rest of the turn is the model's
		if req.ToolChoice != "" && req.ToolChoice != "auto" && req.ToolChoice != "none" {
			req.ToolChoice = "auto"
		}
		if round >= maxToolRounds {
			plan.withdraw(req)
		}
	}
	sink.result(total)
	if out != nil {
		out.Close()
	}
	return used
}

// Adds from's fields to into, later values winning
func mergeExtra(into, from map[string]json.RawMessage) map[string]json.RawMessage {
	if len(from) == 0 {
		return into
	}
	if into == nil {
		into = map[string]json.RawMessage{}
	}
	for k, v := range from {
		into[k] = v
	}
	return into
}

// Reads one answer, thinking and text as they arrive, calls and usage whole, the thinking kept on the result
func (x *exchange) round(resp *http.Response, emit func(chat.Event) error) (*chat.Result, error) {
	defer resp.Body.Close()
	req := x.in.req
	if resp.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		return nil, chat.Refusal(x.runtime, resp.StatusCode, raw)
	}
	if req.Stream {
		var final *chat.Result
		var thinking strings.Builder
		err := x.runtime.ParseStream(resp.Body, func(ev chat.Event) error {
			switch ev.Kind {
			case chat.Started, chat.Said:
				return emit(ev)
			case chat.Thought:
				thinking.WriteString(ev.Text)
				return emit(ev)
			case chat.Stopped:
				final = ev.Res
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if final == nil {
			return nil, errors.New("the stream ended without a stop")
		}
		final.Thinking = thinking.String()
		return final, nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	res, err := x.runtime.ParseResult(req, raw)
	if err != nil {
		if refusal, ok := errors.AsType[*chat.Failure](err); ok && refusal.Code == "" {
			refusal.Code = chat.CodeOf(refusal.Status, raw)
		}
		return nil, err
	}
	if res.Thinking != "" {
		if err := emit(chat.Event{Kind: chat.Thought, Text: res.Thinking}); err != nil {
			return nil, err
		}
	}
	if res.Text != "" {
		if err := emit(chat.Event{Kind: chat.Said, Text: res.Text}); err != nil {
			return nil, err
		}
	}
	return res, nil
}
