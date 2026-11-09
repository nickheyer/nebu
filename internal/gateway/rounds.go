package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/pkg/datalayer"
)

// Opens the runtime's answer to one rendered round, the first round relayed or tried down the decision's endpoints
func (x *exchange) openRound(ctx context.Context, req *chat.Request, path string, body []byte, round int) (*http.Response, error) {
	out := &upstream{method: http.MethodPost, path: path, body: body, stream: req.Stream, header: x.runtimeHeaders()}
	if round > 0 {
		resp, _, err := x.attempt(ctx, out, []datalayer.Endpoint{x.served})
		return resp, err
	}
	var resp *http.Response
	var ep datalayer.Endpoint
	var err error
	if pre := x.prefill(); pre != nil && (req.Kind == chat.Chat || req.Kind == chat.Generate) {
		resp, ep, err = x.relay(ctx, out, pre)
	} else {
		resp, ep, err = x.attempt(ctx, out, x.dec.Endpoints)
	}
	if err != nil {
		return nil, err
	}
	if err := x.received(ctx, resp, ep); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

// Asks the model round by round, running the tools it calls, until it answers or calls one of the client's
func (x *exchange) rounds(ctx context.Context, req *chat.Request) {
	t, sink := x.t, x.sink
	defer sink.close()
	var out chat.Writer
	fail := func(f chat.Failure) {
		if t.Error == "" {
			t.Error = f.Message
		}
		if out == nil {
			x.refuse(f)
			return
		}
		out.Write(chat.Event{Kind: chat.Failed, Failure: &f})
	}
	total := &chat.Result{Model: x.model}
	clientTurns := len(req.Messages)
	var prev *chat.Message
	if clientTurns > 0 {
		last := req.Messages[clientTurns-1]
		prev = &last
	}
	var allSaid strings.Builder
	for round := 0; ; round++ {
		path, body, err := x.runtime.Render(req)
		if err != nil {
			fail(chat.FailureOf(err))
			break
		}
		x.sent = body
		resp, err := x.openRound(ctx, req, path, body, round)
		if err != nil {
			x.resp.Err = err
			fail(x.g.refusal(err, x.model, x.route, x.pool.ID))
			break
		}
		if out == nil && resp.StatusCode < http.StatusMultipleChoices {
			out = x.client.Answer(x.w, req, x.model)
		}
		var said strings.Builder
		res, err := x.round(resp, req, func(ev chat.Event) error {
			if ev.Kind == chat.Said {
				said.WriteString(ev.Text)
			}
			sink.event(ev)
			return out.Write(ev)
		})
		if round == 0 {
			x.firstRound(res)
			if res != nil {
				total.PromptTokenIDs = res.PromptTokenIDs
			}
		}
		allSaid.WriteString(said.String())
		if err != nil {
			x.resp.Err = err
			switch {
			case errors.Is(ctx.Err(), context.DeadlineExceeded):
				fail(upstreamFailure(x.model, ctx.Err()))
			case ctx.Err() != nil:
				t.Stop, t.Error = "cancelled", "client went away"
			default:
				fail(upstreamFailure(x.model, err))
			}
			break
		}
		if total.ID == "" {
			total.ID = res.ID
		}
		total.Usage.In, total.Usage.Out, total.Stop, total.Extra = total.Usage.In+res.Usage.In, total.Usage.Out+res.Usage.Out, res.Stop, mergeExtra(total.Extra, res.Extra)
		total.Usage.Cached += res.Usage.Cached
		total.Usage.DraftOffered, total.Usage.DraftAccepted = total.Usage.DraftOffered+res.Usage.DraftOffered, total.Usage.DraftAccepted+res.Usage.DraftAccepted
		total.Usage.Rounds = append(total.Usage.Rounds, res.Usage.RoundsOrSelf()...)
		if len(res.ToolCalls) > 0 {
			sink.mark()
		}
		var runs []*chat.Run
		var held []chat.ToolCall
		for _, c := range res.ToolCalls {
			pl, mine := x.plan.lookup(c.Name)
			if !mine || x.plan.handsBack(pl) {
				held = append(held, c)
				continue
			}
			run := x.plan.run(ctx, c, round >= maxToolRounds)
			runs = append(runs, run)
			total.Runs = append(total.Runs, run)
			t.ToolCalls = append(t.ToolCalls, traceRun(run))
			if err := out.Write(chat.Event{Kind: chat.Ran, Run: run}); err != nil {
				return
			}
		}
		var parts []chat.Part
		if res.Thinking != "" {
			parts = append(parts, chat.Part{Type: "thinking", Text: res.Thinking})
		}
		if said.Len() > 0 {
			parts = append(parts, chat.Part{Type: "text", Text: said.String()})
		}
		if len(runs) == 0 || len(held) > 0 {
			for i, c := range held {
				if err := out.Write(chat.Event{Kind: chat.Called, Index: i, Tool: &chat.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args}}); err != nil {
					return
				}
			}
			total.ToolCalls = held
			if len(req.Messages) > clientTurns || len(runs) > 0 {
				x.keepRounds(req, clientTurns, prev, allSaid.String(), parts, runs, held)
			}
			if err := out.Write(chat.Event{Kind: chat.Stopped, Res: total}); err != nil {
				if _, ok := errors.AsType[*chat.ToolArgsError](err); ok {
					fail(chat.Failure{Status: http.StatusBadGateway, Code: chat.CodeUpstream, Message: "upstream: " + err.Error()})
				}
			}
			break
		}
		req.Messages = append(append(req.Messages, chat.Message{Role: "assistant", Parts: parts, ToolCalls: res.ToolCalls}), toolTurns(runs)...)
		// A forced first call has been made, the rest of the turn is the model's
		if req.ToolChoice != "" && req.ToolChoice != "auto" && req.ToolChoice != "none" {
			req.ToolChoice = "auto"
		}
		if round >= maxToolRounds {
			x.plan.withdraw(req)
		}
	}
	sink.result(total)
	if out != nil {
		out.Close()
	}
}

// Takes the first round's timing and tokens for the director, tool rounds left out of latency learning
func (x *exchange) firstRound(res *chat.Result) {
	x.resp.FinishedAt = x.g.clock.Now()
	if !x.sink.first.IsZero() {
		x.resp.FirstTokenAt = x.sink.first
	}
	if res != nil {
		x.resp.PromptTokens, x.resp.OutputTokens, x.resp.CachedTokens = res.Usage.In, res.Usage.Out, res.Usage.Cached
		x.sink.firstRound(res.Usage)
	}
}

// The planned tool of a name, false for a client tool or a request with no plan
func (p *toolPlan) lookup(name string) (*planned, bool) {
	if p == nil {
		return nil, false
	}
	pl, ok := p.tools[name]
	return pl, ok
}

// Keeps the answer's tool rounds, the final turn carrying the calls it ran before those handed back
func (x *exchange) keepRounds(req *chat.Request, clientTurns int, prev *chat.Message, text string, parts []chat.Part, runs []*chat.Run, held []chat.ToolCall) {
	before := append([]chat.Message(nil), req.Messages[clientTurns:]...)
	final := chat.Message{Role: "assistant", Parts: parts}
	for _, run := range runs {
		final.ToolCalls = append(final.ToolCalls, run.Call)
	}
	heldFrom := len(final.ToolCalls)
	final.ToolCalls = append(final.ToolCalls, held...)
	x.g.rounds.put(roundKey(x.routeName(), prev, text, held), before, final, toolTurns(runs), heldFrom)
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
func (x *exchange) round(resp *http.Response, req *chat.Request, emit func(chat.Event) error) (*chat.Result, error) {
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		return nil, chat.Refusal(x.runtime, resp.StatusCode, resp.Header, raw)
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
	if res.Text != "" || len(res.TokenIDs) > 0 {
		if err := emit(chat.Event{Kind: chat.Said, Text: res.Text, TokenIDs: res.TokenIDs}); err != nil {
			return nil, err
		}
	}
	return res, nil
}
