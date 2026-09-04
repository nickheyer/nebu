package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Talks to a model through the gateway, one answer or a session over stdin
func runChat(ctx context.Context, e *env, args []string) error {
	fs := e.flags("chat")
	system := fs.String("system", "", "system prompt")
	once := fs.String("once", "", "send this one message and exit")
	temperature := fs.Float64("temperature", -1, "sampling temperature, the runtime default when unset")
	maxTokens := fs.Int("max-tokens", 0, "answer length cap, the runtime default when 0")
	key := fs.String("key", "", "gateway api key, the first configured when empty")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu chat <model> [--system S] [--once TEXT] [--temperature F] [--max-tokens N]")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	if err := e.routeReady(ctx, cl, positional[0]); err != nil {
		return err
	}
	apiKey := *key
	if apiKey == "" && len(e.cfg.GetGateway().GetApiKeys()) > 0 {
		apiKey = e.cfg.GetGateway().GetApiKeys()[0]
	}
	base := e.gatewayBase()
	session := &chatSession{
		out: e.out, url: base + "/v1/chat/completions", model: positional[0], key: apiKey,
		temperature: *temperature, maxTokens: *maxTokens, client: &http.Client{Transport: e.transport(base)},
	}
	if *system != "" {
		session.history = append(session.history, chatMessage{Role: "system", Content: *system})
	}
	if *once != "" {
		return session.ask(ctx, *once)
	}
	fmt.Fprintf(e.errw, "chatting with %s through %s, /reset starts over, /system sets the prompt, ctrl-d ends\n", session.model, session.url)
	sc := bufio.NewScanner(e.in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for {
		fmt.Fprint(e.errw, "> ")
		if !sc.Scan() {
			fmt.Fprintln(e.errw)
			return sc.Err()
		}
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			continue
		case line == "/reset":
			session.reset()
			fmt.Fprintln(e.errw, "history cleared")
			continue
		case strings.HasPrefix(line, "/system "):
			session.reset()
			session.history = append(session.history, chatMessage{Role: "system", Content: strings.TrimSpace(strings.TrimPrefix(line, "/system "))})
			fmt.Fprintln(e.errw, "system prompt set, history cleared")
			continue
		}
		if err := session.ask(ctx, line); err != nil {
			fmt.Fprintln(e.errw, "error:", err)
		}
	}
}

// Fails before a session opens when the route is not there or not ready
func (e *env) routeReady(ctx context.Context, cl *clients, name string) error {
	resp, err := cl.gateway.ListRoutes(ctx, connect.NewRequest(&v1.ListRoutesRequest{}))
	if err != nil {
		return err
	}
	var names []string
	for _, r := range resp.Msg.GetRoutes() {
		if r.GetName() == name {
			if r.GetState() == v1.RouteState_ROUTE_STATE_READY {
				return nil
			}
			return fmt.Errorf("route %s is %s, wait for it or pick another", name, eval.EnumShort(r.GetState()))
		}
		if r.GetState() == v1.RouteState_ROUTE_STATE_READY {
			names = append(names, r.GetName())
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("no route named %s and nothing is ready, start a model with nebu run", name)
	}
	return fmt.Errorf("no route named %s, ready: %s", name, strings.Join(names, ", "))
}

type chatSession struct {
	out         io.Writer
	url         string
	model       string
	key         string
	temperature float64
	maxTokens   int
	client      *http.Client
	history     []chatMessage
}

// Drops every turn but the system prompt
func (s *chatSession) reset() {
	var kept []chatMessage
	for _, m := range s.history {
		if m.Role == "system" {
			kept = append(kept, m)
		}
	}
	s.history = kept
}

// Sends the history with one more user turn and streams the answer to out
func (s *chatSession) ask(ctx context.Context, text string) error {
	s.history = append(s.history, chatMessage{Role: "user", Content: text})
	body := map[string]any{"model": s.model, "messages": s.history, "stream": true, "stream_options": map[string]any{"include_usage": true}}
	if s.temperature >= 0 {
		body["temperature"] = s.temperature
	}
	if s.maxTokens > 0 {
		body["max_tokens"] = s.maxTokens
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.key != "" {
		req.Header.Set("Authorization", "Bearer "+s.key)
	}
	started := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		s.history = s.history[:len(s.history)-1]
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		s.history = s.history[:len(s.history)-1]
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("%d: %s", resp.StatusCode, e.Error.Message)
		}
		return fmt.Errorf("%d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var answer strings.Builder
	tokens := 0
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				Text string `json:"text"`
			} `json:"choices"`
			Usage *struct {
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		for _, c := range chunk.Choices {
			delta := c.Delta.Content + c.Text
			if delta != "" {
				fmt.Fprint(s.out, delta)
				answer.WriteString(delta)
				tokens++
			}
		}
		if chunk.Usage != nil && chunk.Usage.CompletionTokens > 0 {
			tokens = chunk.Usage.CompletionTokens
		}
	}
	fmt.Fprintln(s.out)
	elapsed := time.Since(started).Seconds()
	if elapsed > 0 {
		fmt.Fprintf(s.out, "[%d tokens in %.1fs, %.1f tok/s]\n", tokens, elapsed, float64(tokens)/elapsed)
	}
	s.history = append(s.history, chatMessage{Role: "assistant", Content: answer.String()})
	return sc.Err()
}
