package mcpserver

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	// How long a request to an instance's own server may take and how much of its answer is kept when a call names neither
	instanceTimeout = time.Minute
	instanceAnswer  = 16000
	// Reasoning a chat answers with before the rest is counted
	reasoningShown = 400
)

type listInstancesIn struct {
	RunningOnly bool `json:"running_only,omitempty" jsonschema:"leave out instances that have ended"`
	All         bool `json:"all,omitempty" jsonschema:"answer every ended instance as a whole record, in place of the newest few by how they ended"`
	fullIn
}

type getInstanceIn struct {
	ID   string `json:"id" jsonschema:"instance id or name"`
	Logs uint32 `json:"logs,omitempty" jsonschema:"lines from the end of its log to answer with it"`
	fullIn
}

type instanceLogsIn struct {
	ID    string `json:"id" jsonschema:"instance id or name"`
	Tail  uint32 `json:"tail,omitempty" jsonschema:"lines from the end of the log (default 200)"`
	Match string `json:"match,omitempty" jsonschema:"keep the lines holding this text, whatever its case, read from the whole log"`
}

type instanceGetIn struct {
	ID       string `json:"id" jsonschema:"instance id or name"`
	Path     string `json:"path" jsonschema:"path and query on the instance's own server, such as /props, /slots, /metrics, or /v1/models"`
	MaxBytes uint32 `json:"max_bytes,omitempty" jsonschema:"most of the answer kept, the middle left out of anything longer (default 16000)"`
}

type instanceRequestIn struct {
	instanceGetIn
	Method         string            `json:"method,omitempty" jsonschema:"HTTP method, POST when empty"`
	Body           string            `json:"body,omitempty" jsonschema:"request body, sent as JSON unless a header says otherwise"`
	Headers        map[string]string `json:"headers,omitempty"`
	TimeoutSeconds uint32            `json:"timeout_seconds,omitempty" jsonschema:"longest the request may take (default 60)"`
}

type listParamsIn struct {
	RuntimeID string   `json:"runtime_id" jsonschema:"runtime, such as llamacpp"`
	Search    string   `json:"search,omitempty" jsonschema:"keep the params whose name, flag, or description holds this text"`
	Names     []string `json:"names,omitempty" jsonschema:"answer these params whole, each with its choices, bounds, and rule"`
}

type chatIn struct {
	Model     string         `json:"model" jsonschema:"route name"`
	Prompt    string         `json:"prompt,omitempty"`
	System    string         `json:"system,omitempty"`
	MaxTokens int            `json:"max_tokens,omitempty"`
	Messages  []any          `json:"messages,omitempty" jsonschema:"the whole messages array of an OpenAI chat request, in place of prompt and system"`
	Body      map[string]any `json:"body,omitempty" jsonschema:"any other field of the chat completions request, such as temperature, tools, or response_format, sent as given"`
	PadTokens int            `json:"pad_tokens,omitempty" jsonschema:"about this many tokens of filler put before the prompt, different every call so no cache answers for it, to time how fast a long prompt is read"`
}

// Adds the tools reading one instance, its log, its launch params, and its own server
func (n *nebu) addInstanceTools(s *mcp.Server, ro *mcp.ToolAnnotations) {
	mcp.AddTool(s, &mcp.Tool{Name: "list_instances", Description: "Lists model instances: every one still wanted with its command, params, state, error, plan, and measured memory, then the newest ended ones by what they ran and how they ended", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in listInstancesIn) (*mcp.CallToolResult, any, error) {
		r, err := n.instance.ListInstances(ctx, connect.NewRequest(&v1.ListInstancesRequest{RunningOnly: in.RunningOnly}))
		if err != nil {
			return nil, nil, unwrap(err)
		}
		if in.Full {
			return text(r.Msg), nil, nil
		}
		recs, left := listed(r.Msg.GetInstances(), in.All)
		list := []json.RawMessage{}
		for _, rec := range recs {
			list = append(list, protoJSON(n.lean(rec)))
		}
		if left == 0 {
			return parts("instances", list), nil, nil
		}
		return parts("instances", list, "ended_left_out", left), nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "get_instance", Description: "Reads one instance by id or name: its command, params, state, error, what triage read of a failure, its plan, and the memory it measured, with the end of its log when logs is set", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in getInstanceIn) (*mcp.CallToolResult, any, error) {
		r, err := n.instance.GetInstance(ctx, connect.NewRequest(&v1.GetInstanceRequest{Id: in.ID}))
		if err != nil {
			return nil, nil, unwrap(err)
		}
		rec := r.Msg.GetInstance()
		if !in.Full {
			rec = n.lean(rec).(*v1.Instance)
		}
		if in.Logs == 0 {
			return parts("instance", rec), nil, nil
		}
		lines, err := n.logLines(ctx, rec.GetId(), int(in.Logs), "")
		if err != nil {
			return nil, nil, err
		}
		return parts("instance", rec, "logs", lines), nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "instance_logs", Description: "Gets the end of an instance's log, or the lines of it holding a text. A launch that failed and was tried again keeps the end of each failed try before the latest", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in instanceLogsIn) (*mcp.CallToolResult, any, error) {
		lines, err := n.logLines(ctx, in.ID, cmp.Or(int(in.Tail), tailDefault), in.Match)
		if err != nil {
			return nil, nil, err
		}
		return text(&v1.LogsResponse{Lines: lines}), nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_params", Description: "Lists a runtime's launch params: each name, the flag it sets, its default, and what it does. These are the names params takes. A flag the list lacks still goes in args as the server takes it", Annotations: ro}, n.listParams)
	mcp.AddTool(s, &mcp.Tool{Name: "instance_get", Description: "Reads a path of a running instance's own server, past the gateway, such as llama.cpp's /props for the settings in effect, /slots for each sequence's state, and /metrics", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in instanceGetIn) (*mcp.CallToolResult, any, error) {
		return n.instanceRequest(ctx, instanceRequestIn{instanceGetIn: in, Method: http.MethodGet})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "instance_request", Description: "Sends any request to a running instance's own server, past the gateway, and answers its status and body"}, func(ctx context.Context, _ *mcp.CallToolRequest, in instanceRequestIn) (*mcp.CallToolResult, any, error) {
		return n.instanceRequest(ctx, in)
	})
}

// The end of an instance's log, or of its lines holding a text
func (n *nebu) logLines(ctx context.Context, id string, tail int, match string) ([]string, error) {
	read := tail
	if match != "" {
		read = 0
	}
	var lines []string
	err := n.d.Instances.Logs(ctx, id, false, read, func(got []string) error {
		lines = got
		return nil
	})
	if err != nil || match == "" {
		return lines, err
	}
	want := strings.ToLower(match)
	kept := lines[:0]
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), want) {
			kept = append(kept, line)
		}
	}
	if len(kept) > tail {
		kept = kept[len(kept)-tail:]
	}
	return kept, nil
}

// One line for a param: its name, what sets it, and what it does
func paramLine(p *v1.Param) string {
	var notes []string
	if p.GetFlag() != "" {
		notes = append(notes, p.GetFlag())
	}
	if p.GetEnv() != "" {
		notes = append(notes, "env "+p.GetEnv())
	}
	if standard := cmp.Or(p.GetDefault(), p.GetPlaceholder()); standard != "" {
		notes = append(notes, "default "+standard)
	}
	var choices []string
	for _, c := range p.GetChoices() {
		if c != "" {
			choices = append(choices, c)
		}
	}
	if len(choices) > 0 {
		notes = append(notes, "one of "+strings.Join(choices, " "))
	}
	line := p.GetName()
	if len(notes) > 0 {
		line += " (" + strings.Join(notes, ", ") + ")"
	}
	about := cmp.Or(p.GetDescription(), p.GetLabel())
	if p.GetRule() != "" {
		about += ". Left unset it is " + p.GetRule()
	}
	return line + ": " + about
}

// The launch params of a runtime as lines under their groups, or the named ones whole
func (n *nebu) listParams(_ context.Context, _ *mcp.CallToolRequest, in listParamsIn) (*mcp.CallToolResult, any, error) {
	rt, err := n.d.Runtimes.Get(in.RuntimeID)
	if err != nil {
		return nil, nil, err
	}
	if len(in.Names) > 0 {
		out := &v1.Runtime{Id: rt.ID()}
		for _, p := range rt.Params() {
			if slices.Contains(in.Names, p.GetName()) {
				out.Params = append(out.Params, p)
			}
		}
		if len(out.Params) == 0 {
			return nil, nil, fmt.Errorf("%s has no param named %s", rt.ID(), strings.Join(in.Names, ", "))
		}
		return text(out), nil, nil
	}
	want := strings.ToLower(strings.TrimSpace(in.Search))
	var b strings.Builder
	group, found := "\x00", 0
	for _, p := range rt.Params() {
		if want != "" && !strings.Contains(strings.ToLower(p.GetName()+" "+p.GetFlag()+" "+p.GetLabel()+" "+p.GetDescription()+" "+p.GetRule()), want) {
			continue
		}
		if p.GetGroup() != group {
			group = p.GetGroup()
			fmt.Fprintf(&b, "\n## %s\n", cmp.Or(group, "Other"))
		}
		b.WriteString(paramLine(p) + "\n")
		found++
	}
	if found == 0 {
		return nil, nil, fmt.Errorf("no param of %s holds %q. Its server may still take a flag for it, which goes in args", rt.ID(), in.Search)
	}
	head := fmt.Sprintf("%d launch params of %s. Set one by name in params. A param named env.NAME sets an environment variable of the server, and any flag not listed here goes in args as the server takes it.\n", found, rt.ID())
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: head + b.String()}}}, nil, nil
}

// What an instance answered, its start and its end kept when it answered more than keep bytes
type clipped struct {
	head, tail  []byte
	keep, total int
}

// Keeps a quarter of the room for the start of the output and the rest for its end
func (c *clipped) Write(p []byte) (int, error) {
	c.total += len(p)
	front := c.keep / 4
	rest := p
	if room := front - len(c.head); room > 0 {
		n := min(room, len(rest))
		c.head = append(c.head, rest[:n]...)
		rest = rest[n:]
	}
	c.tail = append(c.tail, rest...)
	if over := len(c.tail) - (c.keep - front); over > 0 {
		c.tail = append([]byte(nil), c.tail[over:]...)
	}
	return len(p), nil
}

// The output as text, and the bytes left out of its middle
func (c *clipped) text() (string, int) {
	cut := c.total - len(c.head) - len(c.tail)
	if cut <= 0 {
		return string(c.head) + string(c.tail), 0
	}
	return fmt.Sprintf("%s\n[... %d bytes left out ...]\n%s", bytes.ToValidUTF8(c.head, nil), cut, bytes.ToValidUTF8(c.tail, nil)), cut
}

// Sends one request to an instance's own server and answers its status and body
func (n *nebu) instanceRequest(ctx context.Context, in instanceRequestIn) (*mcp.CallToolResult, any, error) {
	rec, err := n.d.Instances.Get(in.ID)
	if err != nil {
		return nil, nil, err
	}
	if rec.GetEndpoint() == "" || strings.HasSuffix(rec.GetEndpoint(), ":0") {
		return nil, nil, fmt.Errorf("instance %s is %s and listens nowhere yet", rec.GetName(), strings.TrimPrefix(rec.GetState().String(), "INSTANCE_STATE_"))
	}
	method := cmp.Or(strings.ToUpper(strings.TrimSpace(in.Method)), http.MethodPost)
	rctx, cancel := context.WithTimeout(ctx, cmp.Or(time.Duration(in.TimeoutSeconds)*time.Second, instanceTimeout))
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, method, strings.TrimRight(rec.GetEndpoint(), "/")+"/"+strings.TrimLeft(in.Path, "/"), strings.NewReader(in.Body))
	if err != nil {
		return nil, nil, err
	}
	if in.Body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range in.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	kept := &clipped{keep: cmp.Or(int(in.MaxBytes), instanceAnswer)}
	if _, err := io.Copy(kept, resp.Body); err != nil && kept.total == 0 {
		return nil, nil, err
	}
	body, cut := kept.text()
	answer := []any{"status", resp.StatusCode, "content_type", resp.Header.Get("Content-Type")}
	if cut > 0 {
		answer = append(answer, "left_out_bytes", cut)
	}
	return parts(append(answer, "body", body)...), nil, nil
}

// Words the filler of a padded prompt is drawn from, each about one token to most tokenizers
var fillerWords = strings.Fields("the of and to in is that for it as was with be by on not he this are or his from at which but have an had they you were their one all we can her has there been if more when will would who so no out up into than them only its time new some could these two may then do first any my now such like our over man me even most made after also did many before must through back years where much your way well down should because each just those people how too little state good very make world still own see men work long get here between both life being under never day same another know while last might us great old year off come since against go came right used take three")

// About so many tokens of filler no earlier call sent, so the server reads every one of them
func filler(tokens int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Filler %d, to be ignored:", rand.Uint64())
	for range tokens {
		b.WriteByte(' ')
		b.WriteString(fillerWords[rand.IntN(len(fillerWords))])
	}
	b.WriteString(".\n\n")
	return b.String()
}

// A tool call of a streamed chat answer, its name and arguments joined across the chunks that carry them
type chatCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// One chunk of a streamed chat answer, the fields a timing reads
type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning_content"`
			ToolCalls []struct {
				Index    int      `json:"index"`
				Function chatCall `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		Stop string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		In      int `json:"prompt_tokens"`
		Out     int `json:"completion_tokens"`
		Details struct {
			Cached int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Timings json.RawMessage `json:"timings"`
}

// Asks a served model through the gateway in process, streamed so the reply comes back with how long its first token took and how fast the rest followed
func (n *nebu) chat(ctx context.Context, _ *mcp.CallToolRequest, in chatIn) (*mcp.CallToolResult, any, error) {
	body := map[string]any{}
	switch {
	case len(in.Messages) > 0:
		body["messages"] = in.Messages
	case in.Prompt == "" && in.PadTokens == 0:
		return nil, nil, errors.New("give a prompt or messages")
	default:
		var messages []map[string]string
		if in.System != "" {
			messages = append(messages, map[string]string{"role": "system", "content": in.System})
		}
		prompt := in.Prompt
		if in.PadTokens > 0 {
			prompt = filler(in.PadTokens) + prompt
		}
		body["messages"] = append(messages, map[string]string{"role": "user", "content": prompt})
	}
	if in.MaxTokens > 0 {
		body["max_tokens"] = in.MaxTokens
	}
	for k, v := range in.Body {
		body[k] = v
	}
	body["model"] = in.Model
	body["stream"] = true
	body["stream_options"] = map[string]bool{"include_usage": true}
	payload, _ := json.Marshal(body)
	started := time.Now()
	resp, err := n.send(ctx, "/v1/chat/completions", payload)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	var reply, reasoning strings.Builder
	var first time.Time
	var stop string
	var usage chatChunk
	var calls []*chatCall
	err = chat.ReadSSE(resp.Body, func(_ string, data []byte) error {
		var chunk chatChunk
		// The closing [DONE] is no JSON and is passed over with any other line that is not
		if json.Unmarshal(data, &chunk) != nil {
			return nil
		}
		for _, c := range chunk.Choices {
			if first.IsZero() && (c.Delta.Content != "" || c.Delta.Reasoning != "" || len(c.Delta.ToolCalls) > 0) {
				first = time.Now()
			}
			reply.WriteString(c.Delta.Content)
			reasoning.WriteString(c.Delta.Reasoning)
			for _, tc := range c.Delta.ToolCalls {
				for len(calls) <= tc.Index {
					calls = append(calls, &chatCall{})
				}
				calls[tc.Index].Name += tc.Function.Name
				calls[tc.Index].Arguments += tc.Function.Arguments
			}
			if c.Stop != "" {
				stop = c.Stop
			}
		}
		if chunk.Usage != nil {
			usage.Usage = chunk.Usage
		}
		if len(chunk.Timings) > 0 {
			usage.Timings = chunk.Timings
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	ended := time.Now()
	round := func(v float64) float64 { return float64(int(v*100+0.5)) / 100 }
	answer := []any{"reply", reply.String()}
	if len(calls) > 0 {
		answer = append(answer, "tool_calls", calls)
	}
	if reasoning.Len() > 0 {
		answer = append(answer, "reasoning", clip(reasoning.String(), reasoningShown))
	}
	answer = append(answer, "finish_reason", stop, "total_seconds", round(ended.Sub(started).Seconds()))
	if !first.IsZero() {
		answer = append(answer, "first_token_seconds", round(first.Sub(started).Seconds()))
	}
	if u := usage.Usage; u != nil {
		answer = append(answer, "prompt_tokens", u.In, "completion_tokens", u.Out)
		if u.Details.Cached > 0 {
			answer = append(answer, "cached_prompt_tokens", u.Details.Cached)
		}
		// The wait for the first token is the prompt being read, and the rest is the answer being written
		if wait := first.Sub(started).Seconds(); !first.IsZero() && wait > 0 && u.In-u.Details.Cached > 0 {
			answer = append(answer, "prompt_tokens_per_second", round(float64(u.In-u.Details.Cached)/wait))
		}
		if spent := ended.Sub(first).Seconds(); !first.IsZero() && spent > 0 && u.Out > 1 {
			answer = append(answer, "completion_tokens_per_second", round(float64(u.Out-1)/spent))
		}
	}
	if len(usage.Timings) > 0 {
		answer = append(answer, "runtime_timings", usage.Timings)
	}
	return parts(answer...), nil, nil
}
