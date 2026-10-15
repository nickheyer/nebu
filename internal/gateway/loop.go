package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Opens the runtime's answer to one rendered request, answering the index of the seat that took it
type opener func(ctx context.Context, chat *Chat, path string, body []byte) (*http.Response, context.CancelFunc, int, error)

// Writes an answer round by round, the model's events and the tools the gateway ran between them
type loopWriter interface {
	Write(Event) error
	// Writes a tool the gateway ran, with its answer, in the client's shape when it has one
	ran(run *ToolRun) error
	// Ends the answer with an error
	fail(status int, message, kind string)
	Close() error
}

// A streamed answer in a format that carries no tool the gateway ran
type plainStream struct {
	StreamWriter
}

func (plainStream) ran(*ToolRun) error { return nil }

func (s plainStream) fail(_ int, message, _ string) { s.Write(Event{Kind: "error", Text: message}) }

// A whole answer in a format that carries no tool the gateway ran, rendered at stop
type plainWhole struct {
	w      http.ResponseWriter
	client Flavor
	chat   *Chat
	model  string
	text   strings.Builder
	tools  toolGather
}

func (p *plainWhole) Write(ev Event) error {
	switch ev.Kind {
	case "text":
		p.text.WriteString(ev.Text)
	case "tool":
		p.tools.add(ev)
	case "stop":
		res := ev.Res
		if res == nil {
			res = &Result{}
		}
		out := *res
		out.Model, out.Text, out.ToolCalls = p.model, p.text.String(), p.tools.list()
		out.Stop = stopOf(res.Stop, len(out.ToolCalls))
		answer, err := p.client.RenderResult(p.chat, &out)
		if err != nil {
			return err
		}
		p.w.Header().Set("Content-Type", "application/json")
		p.w.WriteHeader(http.StatusOK)
		_, err = p.w.Write(answer)
		return err
	}
	return nil
}

func (plainWhole) ran(*ToolRun) error { return nil }

func (p *plainWhole) fail(status int, message, kind string) {
	p.client.Error(p.w, status, message, kind)
}

func (plainWhole) Close() error { return nil }

// The writer for a client's format, Anthropic answers carrying the tools the gateway ran as blocks
func loopWriterFor(w http.ResponseWriter, client Flavor, chat *Chat, model string) loopWriter {
	if chat.Format == v1.ApiFlavor_API_FLAVOR_ANTHROPIC {
		if chat.Stream {
			return newAntStream(w, model)
		}
		return &antWhole{w: w, client: client, model: model}
	}
	if chat.Stream {
		return plainStream{client.Stream(w, chat)}
	}
	return &plainWhole{w: w, client: client, chat: chat, model: model}
}

// Serves a chat whose tools the gateway runs, round by round until the model answers or calls a client tool
func (g *Gateway) loop(w *traceWriter, r *http.Request, chat *Chat, plan *toolPlan, name string, open opener, client, upstream Flavor) int {
	t := w.t
	defer plan.close()
	sink := &traceSink{t: t}
	defer sink.close()
	var out loopWriter
	fail := func(status int, message, kind string) {
		t.Error = message
		if out == nil {
			client.Error(w, status, message, kind)
			return
		}
		out.fail(status, message, kind)
	}
	total := &Result{Model: name}
	used := -1
	for round := 0; ; round++ {
		path, body, err := upstream.RenderRequest(chat)
		if err != nil {
			fail(http.StatusBadRequest, err.Error(), "invalid_request_error")
			break
		}
		t.UpstreamRequest = capped(body)
		resp, cancel, i, err := open(r.Context(), chat, path, body)
		if err != nil {
			g.log.Warn("gateway loop", "model", name, "err", err)
			fail(upstreamFailure(name, err))
			break
		}
		used = i
		if out == nil && resp.StatusCode < http.StatusMultipleChoices {
			t.FirstByteAt = timestamppb.Now()
			relayHeaders(w.Header(), resp.Header)
			out = loopWriterFor(w, client, chat, name)
		}
		var said strings.Builder
		res, err := g.round(chat, resp, upstream, func(text string) error {
			said.WriteString(text)
			sink.event(Event{Kind: "text", Text: text})
			return out.Write(Event{Kind: "text", Text: text})
		})
		cancel()
		if err != nil {
			switch {
			case r.Context().Err() != nil:
				t.Stop, t.Error = "cancelled", "client went away"
				g.log.Info("gateway loop", "model", name, "err", t.Error)
			case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
				fail(http.StatusGatewayTimeout, fmt.Sprintf("model %s did not answer within its timeout", name), "timeout_error")
			default:
				g.log.Warn("gateway loop", "model", name, "err", err)
				if refusal, ok := errors.AsType[*upstreamRefusal](err); ok {
					fail(refusal.status, refusal.message, refusal.kind)
					t.Error = "upstream: " + refusal.message
				} else {
					fail(http.StatusBadGateway, "upstream: "+err.Error(), "server_error")
				}
			}
			break
		}
		total.In, total.Out, total.Stop, total.Extra = total.In+res.In, total.Out+res.Out, res.Stop, mergeExtra(total.Extra, res.Extra)
		total.DraftOffered, total.DraftAccepted = total.DraftOffered+res.DraftOffered, total.DraftAccepted+res.DraftAccepted
		if len(res.ToolCalls) > 0 {
			sink.first()
		}
		var runs []*ToolRun
		var held []ToolCall
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
			if err := out.ran(run); err != nil {
				return used
			}
		}
		if len(runs) == 0 || len(held) > 0 {
			for i, c := range held {
				if err := out.Write(Event{Kind: "tool", Index: i, Tool: &ToolCall{ID: c.ID, Name: c.Name, Args: c.Args}}); err != nil {
					return used
				}
			}
			total.ToolCalls = held
			if err := out.Write(Event{Kind: "stop", Res: total}); err != nil {
				if _, ok := errors.AsType[*toolArgsError](err); ok {
					fail(http.StatusBadGateway, "upstream: "+err.Error(), "upstream_error")
				}
			}
			break
		}
		var parts []Part
		if said.Len() > 0 {
			parts = []Part{{Type: "text", Text: said.String()}}
		}
		chat.Messages = append(append(chat.Messages, Message{Role: "assistant", Parts: parts, ToolCalls: res.ToolCalls}), toolTurns(runs)...)
		// A forced first call has been made, the rest of the turn is the model's
		if chat.ToolChoice != "" && chat.ToolChoice != "auto" && chat.ToolChoice != "none" {
			chat.ToolChoice = "auto"
		}
		if round >= maxToolRounds {
			plan.withdraw(chat)
		}
	}
	sink.result(total)
	if out != nil {
		out.Close()
	}
	return used
}

// Reads one answer of the model, text as it arrives and calls and usage whole
func (g *Gateway) round(chat *Chat, resp *http.Response, upstream Flavor, text func(string) error) (*Result, error) {
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		message := upstream.ErrorMessage(raw)
		if message == "" {
			message = strings.TrimSpace(string(raw))
		}
		return nil, &upstreamRefusal{status: resp.StatusCode, message: message, kind: errorKind(resp.StatusCode, raw)}
	}
	if chat.Stream {
		var final *Result
		err := upstream.ParseStream(resp.Body, func(ev Event) error {
			switch ev.Kind {
			case "text":
				return text(ev.Text)
			case "stop":
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
		return final, nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	res, err := upstream.ParseResult(chat, raw)
	if err != nil {
		if refusal, ok := errors.AsType[*upstreamRefusal](err); ok && refusal.kind == "" {
			refusal.kind = errorKind(refusal.status, raw)
		}
		return nil, err
	}
	if res.Text != "" {
		if err := text(res.Text); err != nil {
			return nil, err
		}
	}
	return res, nil
}
