package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"sync"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/gateway/jinja"
	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/tokenizer"
	"github.com/nickheyer/nebu/pkg/transcode"
	"github.com/nickheyer/nebu/pkg/transcode/vllm"
)

// Tokenizers loaded once per tokenizer.json and shared by every instance serving the same model files
type tokenizers struct {
	file func(instanceID string) (string, error)
	mu   sync.Mutex
	// The load of one file, joined by every Get that arrives while it runs
	loads map[string]*tokenizerLoad
	// The file each instance's tokenizer was located at
	files map[string]string
}

type tokenizerLoad struct {
	done chan struct{}
	tok  *tokenizer.Tokenizer
	err  error
}

func newTokenizers(file func(instanceID string) (string, error)) *tokenizers {
	return &tokenizers{file: file, loads: map[string]*tokenizerLoad{}, files: map[string]string{}}
}

// The tokenizer of an instance's model files, located on first use and loaded once per file
func (c *tokenizers) Get(instanceID string) (*tokenizer.Tokenizer, error) {
	c.mu.Lock()
	path, known := c.files[instanceID]
	c.mu.Unlock()
	if !known {
		var err error
		if path, err = c.file(instanceID); err != nil {
			return nil, fmt.Errorf("tokenizer of %s: %w", instanceID, err)
		}
	}
	c.mu.Lock()
	c.files[instanceID] = path
	l, loading := c.loads[path]
	if !loading {
		l = &tokenizerLoad{done: make(chan struct{})}
		c.loads[path] = l
	}
	c.mu.Unlock()
	if loading {
		<-l.done
		return l.tok, l.err
	}
	l.tok, l.err = tokenizer.Load(path)
	close(l.done)
	if l.err != nil {
		c.mu.Lock()
		if c.loads[path] == l {
			delete(c.loads, path)
		}
		c.mu.Unlock()
	}
	return l.tok, l.err
}

// Drops the instances not among the live ones and unloads every file no live instance uses
func (c *tokenizers) keep(live map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	used := map[string]bool{}
	for id, path := range c.files {
		if !live[id] {
			delete(c.files, id)
			continue
		}
		used[path] = true
	}
	for path := range c.loads {
		if !used[path] {
			delete(c.loads, path)
		}
	}
}

// The instance id of every endpoint the store holds
func (g *Gateway) liveInstances() map[string]bool {
	live := map[string]bool{}
	for _, ep := range g.store.Endpoints("", nil) {
		live[ep.Labels()[datalayer.LabelInstance]] = true
	}
	return live
}

// A loaded tokenizer as the transcoder's prompt encoder and incremental answer decoder
type modelTokens struct{ t *tokenizer.Tokenizer }

func (m modelTokens) Encode(text string, addSpecialTokens bool) ([]uint32, error) {
	return m.t.Encode(text, addSpecialTokens)
}

// Decodes with special tokens kept, as the HF decode of logprob tokens and echoed prompts does
func (m modelTokens) Decode(ids []uint32) (string, error) { return m.t.Decode(ids, false) }

func (m modelTokens) Stream(skipSpecialTokens bool) vllm.TokenStream {
	return m.t.NewStream(skipSpecialTokens)
}

// One request's encoder, answering the ids it already computed for the same text and special token choice
type promptMemo struct {
	modelTokens
	mu      sync.Mutex
	text    string
	special bool
	ids     []uint32
}

func (m *promptMemo) Encode(text string, addSpecialTokens bool) ([]uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ids != nil && m.text == text && m.special == addSpecialTokens {
		return m.ids, nil
	}
	ids, err := m.modelTokens.Encode(text, addSpecialTokens)
	if err != nil {
		return nil, err
	}
	m.text, m.special, m.ids = text, addSpecialTokens, ids
	return ids, nil
}

// The request's encoder over an instance's tokenizer, kept across the decision and the send while the files agree
func (x *exchange) promptEncoder(instanceID string) (*promptMemo, error) {
	tok, err := x.g.tokenizers.Get(instanceID)
	if err != nil {
		return nil, err
	}
	if x.prompts == nil || x.prompts.t != tok {
		x.prompts = &promptMemo{modelTokens: modelTokens{t: tok}}
	}
	return x.prompts, nil
}

// Whether prompts leave the gateway as token ids over OpenAI JSON, the vLLM gRPC transcoder encoding its own
func (x *exchange) tokensMode() bool {
	return x.pool.TokensInOut && !(x.pool.AppProtocol == datalayer.AppProtocolGRPC && x.pool.Runtime == runtimeVLLM)
}

// Hands the vLLM call the pool's tokenizer in tokens in tokens out mode, the engine then taking and answering ids
func (x *exchange) tokensInOutCall(call *vllm.Call, ep datalayer.Endpoint) error {
	if !x.pool.TokensInOut {
		return nil
	}
	enc, err := x.promptEncoder(ep.Labels()[datalayer.LabelInstance])
	if err != nil {
		return transcode.NewError(http.StatusInternalServerError, "tokens in tokens out for "+ep.Address().String()+": "+err.Error())
	}
	call.Tokenizer, call.Detokenizer = enc, enc
	return nil
}

// The runtime of a tokens in tokens out pool reached over OpenAI JSON, encoding with the picked instance's files
func (x *exchange) tokensRuntime(rt chat.Runtime) (chat.Runtime, error) {
	instance := x.dec.Endpoints[0].Labels()[datalayer.LabelInstance]
	enc, err := x.promptEncoder(instance)
	if err != nil {
		return nil, serverFailure("tokens in tokens out for " + x.model + ": " + err.Error())
	}
	return tokensRuntime{Runtime: rt, enc: enc, runtime: x.pool.Runtime, template: func() (*jinja.Template, error) { return x.g.templates.Get(instance) }}, nil
}

// Whether offered tools go into the prompt rather than to the endpoint, SGLang reading them beside an input_ids prompt
func foldsTools(support v1.ToolSupport, pool datalayer.Pool) bool {
	return support == v1.ToolSupport_TOOL_SUPPORT_EMULATED || (pool.TokensInOut && pool.Runtime != runtimeSGLang)
}

// Whether the runtime codec renders the request as a chat the template wraps
func chatLike(req *chat.Request) bool {
	return req.Kind == chat.Chat || (req.Kind == chat.Generate && !req.Raw)
}

// Fields of a chat body the template or the gateway consumed, which a completions body must not carry
var chatOnlyFields = map[string]bool{
	"add_generation_prompt": true, "continue_final_message": true, "chat_template": true, "chat_template_kwargs": true,
	"documents": true, "echo": true, "reasoning_effort": true, "include_reasoning": true, "logprobs": true, "top_logprobs": true,
	"parallel_tool_calls": true,
}

// A runtime whose prompts travel as token ids, chats rendered through the chat template and encoded at the gateway
type tokensRuntime struct {
	chat.Runtime
	enc      vllm.Tokenizer
	runtime  string
	template func() (*jinja.Template, error)
}

func (t tokensRuntime) Render(req *chat.Request) (string, []byte, error) {
	switch {
	case chatLike(req):
		sglang := t.runtime == runtimeSGLang
		if raw, ok := req.Extra["logprobs"]; ok && !sglang && !bytes.Equal(bytes.TrimSpace(raw), []byte("false")) && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return "", nil, bad("logprobs on a chat are refused in tokens in tokens out mode over a vLLM OpenAI JSON endpoint, the completions API shapes them differently, serve the pool over vLLM gRPC for them")
		}
		tpl, err := t.template()
		if err != nil {
			return "", nil, serverFailure("no chat template for " + req.Model + ": " + err.Error())
		}
		path, body, err := t.Runtime.Render(req)
		if err != nil {
			return "", nil, err
		}
		prompt, err := promptOf(path, body, tpl)
		if err != nil {
			return "", nil, err
		}
		ids, err := t.encode(prompt, false)
		if err != nil {
			return "", nil, err
		}
		if sglang {
			body, err = withInputIDs(body, ids)
			return path, body, err
		}
		return t.Runtime.Render(tokenPrompt(req, ids))
	case req.Kind == chat.Generate:
		out := *req
		out.Messages = make([]chat.Message, len(req.Messages))
		for i, m := range req.Messages {
			out.Messages[i] = m
			if len(m.Parts) != 1 || m.Parts[0].Type != "text" {
				continue
			}
			ids, err := t.encode(m.Parts[0].Text, true)
			if err != nil {
				return "", nil, err
			}
			out.Messages[i].Parts = []chat.Part{{Type: "tokens", Tokens: ints(ids)}}
		}
		return t.Runtime.Render(&out)
	case req.Kind == chat.Embed:
		out := *req
		out.Inputs = make([]chat.Part, len(req.Inputs))
		for i, p := range req.Inputs {
			out.Inputs[i] = p
			if p.Type != "text" {
				continue
			}
			ids, err := t.encode(p.Text, true)
			if err != nil {
				return "", nil, err
			}
			out.Inputs[i] = chat.Part{Type: "tokens", Tokens: ints(ids)}
		}
		return t.Runtime.Render(&out)
	}
	return t.Runtime.Render(req)
}

// Takes no request natively, since every prompt is encoded before it leaves the gateway
func (t tokensRuntime) Native(*chat.Request) (string, []byte, error) {
	return "", nil, bad("tokens in tokens out encodes every prompt, so the request needs translating")
}

// The ids of a text, an empty encoding refused as the engine would refuse an empty prompt
func (t tokensRuntime) encode(text string, addSpecialTokens bool) ([]uint32, error) {
	ids, err := encodeOrFail(t.enc, text, addSpecialTokens)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, bad("the prompt tokenizes to no tokens")
	}
	return ids, nil
}

// The prompt the template renders for a chat, from the OpenAI body the runtime codec writes for it
func chatPrompt(req *chat.Request, rt chat.Runtime, tpl *jinja.Template) (string, error) {
	path, body, err := rt.Render(req)
	if err != nil {
		return "", err
	}
	return promptOf(path, body, tpl)
}

// The chat body with the prompt ids under input_ids, SGLang still reading the messages for tools and stops
func withInputIDs(body []byte, ids []uint32) ([]byte, error) {
	doc, err := parseDoc(chat.OpenAIChat, body)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 0, len(ids)*6+2)
	raw = append(raw, '[')
	for i, id := range ids {
		if i > 0 {
			raw = append(raw, ',')
		}
		raw = strconv.AppendUint(raw, uint64(id), 10)
	}
	raw = append(raw, ']')
	out, err := doc.WithField("input_ids", raw)
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// The prompt the template renders for a rendered OpenAI chat body
func promptOf(path string, body []byte, tpl *jinja.Template) (string, error) {
	if path != chat.OpenAIChat {
		return "", bad("tokens in tokens out needs an OpenAI JSON or vLLM gRPC endpoint, this pool's runtime renders chats as %s", path)
	}
	stripped, kwargs, err := templateKwargs(body, nil)
	if err != nil {
		return "", bad("chat_template_kwargs: %v", err)
	}
	prompt, err := vllm.ChatPrompt(stripped, tpl.With(kwargs))
	if err != nil {
		return "", transcodeFailure(err)
	}
	return prompt, nil
}

// A transcoder refusal as the failure the client sees, its status and OpenAI error body kept
func transcodeFailure(err error) error {
	if te, ok := err.(*transcode.Error); ok {
		return chat.Refusal(chat.RuntimeOf(v1.ApiFlavor_API_FLAVOR_OPENAI), te.Status, http.Header{}, te.Body)
	}
	return err
}

// The chat as the raw completions request carrying its rendered prompt as token ids
func tokenPrompt(req *chat.Request, ids []uint32) *chat.Request {
	out := *req
	out.Kind, out.Raw = chat.Generate, true
	out.Messages = []chat.Message{{Role: "user", Parts: []chat.Part{{Type: "tokens", Tokens: ints(ids)}}}}
	out.Tools, out.ToolChoice, out.Sources, out.Origin = nil, "", nil, nil
	out.Extra = maps.Clone(req.Extra)
	maps.DeleteFunc(out.Extra, func(k string, _ json.RawMessage) bool { return chatOnlyFields[k] })
	return &out
}

func ints(ids []uint32) []int {
	out := make([]int, len(ids))
	for i, id := range ids {
		out[i] = int(id)
	}
	return out
}

// The OpenAI path a body kind stands for, empty for kinds the OpenAI protocol does not carry
func openAIPath(kind bodyparse.Kind) string {
	switch kind {
	case bodyparse.KindChatCompletions:
		return chat.OpenAIChat
	case bodyparse.KindCompletions:
		return chat.OpenAICompletions
	case bodyparse.KindEmbeddings:
		return chat.OpenAIEmbeddings
	}
	return ""
}

// A fault of the gateway's own files or tokenizer, answered as a server error rather than an upstream one
func serverFailure(msg string) *chat.Failure {
	return &chat.Failure{Status: http.StatusInternalServerError, Code: chat.CodeServer, Message: msg}
}

// The ids of the exact prompt the endpoint receives for the body, nil when it carries no single prompt
func (x *exchange) promptIDs(doc bodyparse.Document, instanceID string) ([]uint32, error) {
	enc, err := x.promptEncoder(instanceID)
	if err != nil {
		return nil, serverFailure("tokens in tokens out for " + x.model + ": " + err.Error())
	}
	client, path := x.client, x.r.URL.Path
	if client == nil {
		client, path = chat.ClientOf(v1.ApiFlavor_API_FLAVOR_OPENAI), openAIPath(doc.Kind())
		if path == "" {
			return nil, nil
		}
	}
	creq, err := chatOf(doc, client, path)
	if err != nil {
		return nil, nil
	}
	switch {
	case chatLike(creq):
		rt := chat.RuntimeOf(x.g.runtimes.API(x.pool.Runtime))
		if len(creq.Tools) > 0 && foldsTools(x.g.instances.ToolSupport(instanceID), x.pool) {
			rt = chat.Emulate(rt)
		}
		tpl, err := x.g.templates.Get(instanceID)
		if err != nil {
			return nil, serverFailure("no chat template for " + x.model + ": " + err.Error())
		}
		prompt, err := chatPrompt(creq, rt, tpl)
		if err != nil {
			return nil, err
		}
		return encodeOrFail(enc, prompt, false)
	case creq.Kind == chat.Generate && len(creq.Messages) == 1 && len(creq.Messages[0].Parts) == 1 && creq.Messages[0].Parts[0].Type == "text":
		return encodeOrFail(enc, creq.Messages[0].Parts[0].Text, true)
	}
	return nil, nil
}

// The ids of a text, a tokenizer fault answered as a server error
func encodeOrFail(enc vllm.Tokenizer, text string, addSpecialTokens bool) ([]uint32, error) {
	ids, err := enc.Encode(text, addSpecialTokens)
	if err != nil {
		return nil, serverFailure("tokenize prompt: " + err.Error())
	}
	return ids, nil
}

// Encodes the exact prompt the endpoint will receive before scheduling, so prefix scoring hashes token ids
type tokenize struct{ g *Gateway }

func (*tokenize) TypedName() framework.TypedName {
	return framework.TypedName{Type: "tokenize", Name: "tokenize"}
}

func (p *tokenize) PreRequest(ctx context.Context, in *requestcontrol.Inbound, req *framework.Request) error {
	x := exchangeOf(ctx)
	if x == nil || !x.pool.TokensInOut || in.Body == nil || len(req.PromptTokenIDs) > 0 {
		return nil
	}
	ready := p.g.store.Endpoints(x.pool.ID, datalayer.ReadyAndHealthy)
	if len(ready) == 0 {
		return nil
	}
	ids, err := x.promptIDs(in.Body, ready[0].Labels()[datalayer.LabelInstance])
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	req.PromptTokenIDs, req.PromptTokens = ints(ids), len(ids)
	return nil
}
