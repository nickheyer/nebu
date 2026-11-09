package vllm

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"

	"github.com/nickheyer/nebu/pkg/transcode"
	vllmpb "github.com/nickheyer/nebu/pkg/transcode/gen/vllm"
)

// An OpenAI chat completions request with the vLLM extensions the transcoder reads
type chatRequest struct {
	params
	Messages             []Message       `json:"messages"`
	Tools                []Tool          `json:"tools"`
	ToolChoice           json.RawMessage `json:"tool_choice"`
	ParallelToolCalls    *bool           `json:"parallel_tool_calls"`
	AddGenerationPrompt  *bool           `json:"add_generation_prompt"`
	ContinueFinalMessage bool            `json:"continue_final_message"`
	ChatTemplate         *string         `json:"chat_template"`
	ChatTemplateKwargs   map[string]any  `json:"chat_template_kwargs"`
	Documents            json.RawMessage `json:"documents"`
	Echo                 bool            `json:"echo"`
	ReasoningEffort      *string         `json:"reasoning_effort"`
	IncludeReasoning     *bool           `json:"include_reasoning"`
	Logprobs             bool            `json:"logprobs"`
	TopLogprobs          *int64          `json:"top_logprobs"`
	MaxTokens            *int64          `json:"max_tokens"`
	MaxCompletionTokens  *int64          `json:"max_completion_tokens"`
}

// Refuses chat options the host renderer signature cannot carry to the template
func (r *chatRequest) refuse() error {
	switch {
	case len(r.Messages) == 0:
		return transcode.BadRequest("messages must not be empty")
	case r.ContinueFinalMessage:
		return transcode.BadRequest("continue_final_message is refused, the host template renders whole turns")
	case r.ChatTemplate != nil:
		return transcode.BadRequest("chat_template is refused, the host owns the model chat template")
	case len(r.ChatTemplateKwargs) > 0:
		return transcode.BadRequest("chat_template_kwargs is refused, the host renderer takes no template arguments")
	case len(r.Documents) > 0 && string(r.Documents) != "null":
		return transcode.BadRequest("documents is refused, the host renderer takes no documents")
	case r.Echo:
		return transcode.BadRequest("echo is refused on chat completions")
	case r.ReasoningEffort != nil:
		return transcode.BadRequest("reasoning_effort is refused, the host renderer takes no template arguments")
	case r.IncludeReasoning != nil && !*r.IncludeReasoning:
		return transcode.BadRequest("include_reasoning false is refused, the transcoder runs no reasoning parser")
	case r.TopLogprobs != nil && !r.Logprobs:
		return transcode.BadRequest("top_logprobs needs logprobs set to true")
	case r.TopLogprobs != nil && (*r.TopLogprobs < 0 || *r.TopLogprobs > math.MaxInt32):
		return transcode.BadRequest("top_logprobs must be a non-negative count")
	}
	return nil
}

// The prompt text the template renders for the chat, with the generation prompt unless the body turns it off
func (r *chatRequest) prompt(template Renderer) (string, error) {
	prompt, err := template.Render(r.Messages, r.Tools, r.AddGenerationPrompt == nil || *r.AddGenerationPrompt)
	if err != nil {
		return "", transcode.BadRequest("chat template: %v", err)
	}
	return prompt, nil
}

// The prompt ChatCompletions would send for a chat body, refusing the body exactly as the call would
func ChatPrompt(body []byte, template Renderer) (string, error) {
	if template == nil {
		return "", transcode.NewError(http.StatusInternalServerError, "no chat template renderer for this model")
	}
	var r chatRequest
	if err := decode(body, &r); err != nil {
		return "", err
	}
	if err := r.refuse(); err != nil {
		return "", err
	}
	return r.prompt(template)
}

// Renders the chat with the host template and answers as an OpenAI chat completion or SSE stream
func (c *Client) ChatCompletions(ctx context.Context, body []byte, template Renderer, stream bool, call Call) (*transcode.Reply, error) {
	if template == nil {
		return nil, transcode.NewError(http.StatusInternalServerError, "no chat template renderer for this model")
	}
	var r chatRequest
	if err := decode(body, &r); err != nil {
		return nil, err
	}
	if err := r.refuse(); err != nil {
		return nil, err
	}
	rid := call.requestID()
	plan, schema, err := planTools(r.Tools, r.ToolChoice, r.ParallelToolCalls, call.Tools, rid)
	if err != nil {
		return nil, err
	}
	s := shape{toolSchema: schema}
	switch {
	case r.MaxCompletionTokens != nil:
		s.maxTokens, err = maxTokens("max_completion_tokens", *r.MaxCompletionTokens)
	case r.MaxTokens != nil:
		s.maxTokens, err = maxTokens("max_tokens", *r.MaxTokens)
	}
	if err != nil {
		return nil, err
	}
	a := &chat{id: "chatcmpl-" + rid, created: c.now().Unix(), model: r.Model, tools: plan, tokenIDs: r.ReturnTokenIDs}
	s.tokenIDs = r.ReturnTokenIDs
	if a.detok, err = call.tokensOut(); err != nil {
		return nil, err
	}
	s.tokensOut = a.detok != nil
	a.skip = r.SkipSpecialTokens == nil || *r.SkipSpecialTokens
	if r.Logprobs {
		top := 0
		if r.TopLogprobs != nil {
			top = int(*r.TopLogprobs)
		}
		if a.lp, err = newLogprobs(top, r.ReturnTokensAsTokenIDs, call.Detokenizer); err != nil {
			return nil, err
		}
		s.logprobs, s.topN = true, uint32(top)
	}
	if o := r.StreamOptions; o != nil {
		a.includeUsage, a.continuous = o.IncludeUsage, o.IncludeUsage && o.ContinuousUsageStats
	}
	req, err := r.generate(call, rid, stream, s)
	if err != nil {
		return nil, err
	}
	prompt, err := r.prompt(template)
	if err != nil {
		return nil, err
	}
	req.Prompt = &vllmpb.GenerateRequest_Text{Text: prompt}
	if call.Tokenizer != nil {
		ids, err := call.Tokenizer.Encode(prompt, false)
		if err != nil {
			return nil, transcode.NewError(http.StatusInternalServerError, fmt.Sprintf("tokenize prompt: %v", err))
		}
		if len(ids) == 0 {
			return nil, transcode.BadRequest("the rendered chat tokenizes to no tokens")
		}
		req.Prompt = &vllmpb.GenerateRequest_TokenIds{TokenIds: &vllmpb.TokenIds{Ids: ids}}
	}
	if stream {
		return c.stream(ctx, req, a)
	}
	return c.unary(ctx, req, a)
}

type chatCompletion struct {
	ID               string         `json:"id"`
	Object           string         `json:"object"`
	Created          int64          `json:"created"`
	Model            string         `json:"model"`
	Choices          []chatChoice   `json:"choices"`
	Usage            *usage         `json:"usage"`
	KVTransferParams map[string]any `json:"kv_transfer_params,omitempty"`
	PromptTokenIDs   []uint32       `json:"prompt_token_ids,omitempty"`
}

type chatChoice struct {
	Index        int             `json:"index"`
	Message      chatMessage     `json:"message"`
	Logprobs     *chatLogprobs   `json:"logprobs"`
	FinishReason string          `json:"finish_reason"`
	StopReason   json.RawMessage `json:"stop_reason"`
	TokenIDs     []uint32        `json:"token_ids,omitempty"`
}

type chatMessage struct {
	Role      string     `json:"role"`
	Content   *string    `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type chatChunk struct {
	ID             string        `json:"id"`
	Object         string        `json:"object"`
	Created        int64         `json:"created"`
	Model          string        `json:"model"`
	Choices        []chunkChoice `json:"choices"`
	Usage          *usage        `json:"usage,omitempty"`
	PromptTokenIDs []uint32      `json:"prompt_token_ids,omitempty"`
}

type chunkChoice struct {
	Index        int             `json:"index"`
	Delta        chatDelta       `json:"delta"`
	Logprobs     *chatLogprobs   `json:"logprobs"`
	FinishReason *string         `json:"finish_reason"`
	StopReason   json.RawMessage `json:"stop_reason,omitempty"`
	TokenIDs     []uint32        `json:"token_ids,omitempty"`
}

type chatDelta struct {
	Role      string          `json:"role,omitempty"`
	Content   *string         `json:"content,omitempty"`
	ToolCalls []toolCallDelta `json:"tool_calls,omitempty"`
}

type toolCallDelta struct {
	Index    int           `json:"index"`
	ID       string        `json:"id,omitempty"`
	Type     string        `json:"type,omitempty"`
	Function functionDelta `json:"function"`
}

type functionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// Renders vLLM answers as chat.completion bodies and chat.completion.chunk events
type chat struct {
	id           string
	model        string
	created      int64
	tools        toolPlan
	lp           *logprobs
	includeUsage bool
	continuous   bool
	started      bool
	prompt       uint32
	generated    uint32
	st           toolState
	finish       *vllmpb.FinishInfo
	// Whether choices carry their token ids, and the host side decoding of tokens out mode
	tokenIDs  bool
	detok     StreamDetokenizer
	skip      bool
	tstream   TokenStream
	promptIDs []uint32
}

// The text of a sequence output, decoded by the host from its token ids in tokens out mode
func (a *chat) text(seq *vllmpb.SequenceOutput, whole bool) (string, error) {
	return outputText(a.detok, &a.tstream, a.skip, seq, whole)
}

// Builds the chat.completion body with message, tool calls, logprobs, finish and usage
func (a *chat) whole(resp *vllmpb.GenerateResponse) ([]byte, error) {
	seq := resp.GetOutputs()
	fi := seq.GetFinishInfo()
	if fi == nil || resp.GetPromptInfo() == nil {
		return nil, transcode.BadGateway("backend answer lacks prompt or finish info")
	}
	reason, stop, err := finishReason(fi)
	if err != nil {
		return nil, err
	}
	text, err := a.text(seq, true)
	if err != nil {
		return nil, err
	}
	content, calls, err := a.tools.final(text, fi.GetFinishReason() == vllmpb.FinishInfo_STOP)
	if err != nil {
		return nil, err
	}
	if reason == "stop" && a.tools.called(len(calls)) {
		reason = "tool_calls"
	}
	choice := chatChoice{
		Index:        int(seq.GetIndex()),
		Message:      chatMessage{Role: "assistant", Content: content, ToolCalls: calls},
		FinishReason: reason,
		StopReason:   stop,
	}
	var promptIDs []uint32
	if a.tokenIDs {
		choice.TokenIDs, promptIDs = seq.GetTokenIds(), resp.GetPromptInfo().GetTokenIds()
	}
	if a.lp != nil {
		entries, err := a.lp.chat(seq)
		if err != nil {
			return nil, err
		}
		choice.Logprobs = &chatLogprobs{Content: entries}
	}
	return json.Marshal(chatCompletion{
		ID:               a.id,
		Object:           "chat.completion",
		Created:          a.created,
		Model:            a.model,
		Choices:          []chatChoice{choice},
		Usage:            newUsage(resp.GetPromptInfo().GetNumPromptTokens(), fi.GetNumOutputTokens(), fi),
		KVTransferParams: kvParams(fi),
		PromptTokenIDs:   promptIDs,
	})
}

// Emits the role chunk first, then content, tool call and logprob deltas, with finish_reason on the last
func (a *chat) next(msg *vllmpb.GenerateResponse) ([][]byte, bool, error) {
	var out []any
	pi := msg.GetPromptInfo()
	if pi != nil {
		a.prompt = pi.GetNumPromptTokens()
		if a.tokenIDs {
			a.promptIDs = pi.GetTokenIds()
		}
	}
	if !a.started {
		if pi == nil && a.includeUsage {
			return nil, false, transcode.BadGateway("backend stream lacks prompt info for usage")
		}
		a.started = true
		empty := ""
		first := a.chunk(chunkChoice{Delta: chatDelta{Role: "assistant", Content: &empty}})
		first.PromptTokenIDs, a.promptIDs = a.promptIDs, nil
		out = append(out, first)
	}
	if seq := msg.GetOutputs(); seq != nil {
		a.generated += seq.GetNumTokens()
		fi := seq.GetFinishInfo()
		text, err := a.text(seq, false)
		if err != nil {
			return nil, false, err
		}
		content, calls, err := a.tools.delta(&a.st, text, fi != nil, fi.GetFinishReason() == vllmpb.FinishInfo_STOP)
		if err != nil {
			return nil, false, err
		}
		choice := chunkChoice{Index: int(seq.GetIndex()), Delta: chatDelta{ToolCalls: calls}}
		if a.tokenIDs {
			choice.TokenIDs = seq.GetTokenIds()
		}
		if content != "" {
			choice.Delta.Content = &content
		}
		if a.lp != nil && len(seq.GetTokenIds()) > 0 {
			entries, err := a.lp.chat(seq)
			if err != nil {
				return nil, false, err
			}
			choice.Logprobs = &chatLogprobs{Content: entries}
		}
		if fi != nil {
			reason, stop, err := finishReason(fi)
			if err != nil {
				return nil, false, err
			}
			if reason == "stop" && a.tools.called(a.st.sentCalls) {
				reason = "tool_calls"
			}
			choice.FinishReason, choice.StopReason = &reason, stop
			a.finish = fi
		}
		if content != "" || len(calls) > 0 || choice.Logprobs != nil || fi != nil || len(choice.TokenIDs) > 0 {
			out = append(out, a.chunk(choice))
		}
	}
	p, err := payloads(out...)
	return p, a.finish != nil, err
}

// Returns the final usage chunk when stream_options asked for it
func (a *chat) tail() ([][]byte, error) {
	if !a.includeUsage {
		return nil, nil
	}
	return payloads(chatChunk{
		ID:      a.id,
		Object:  "chat.completion.chunk",
		Created: a.created,
		Model:   a.model,
		Choices: []chunkChoice{},
		Usage:   newUsage(a.prompt, a.finish.GetNumOutputTokens(), a.finish),
	})
}

// Wraps one choice in a chunk carrying running usage when continuous usage stats are on
func (a *chat) chunk(choice chunkChoice) chatChunk {
	c := chatChunk{ID: a.id, Object: "chat.completion.chunk", Created: a.created, Model: a.model, Choices: []chunkChoice{choice}}
	if a.continuous {
		c.Usage = newUsage(a.prompt, a.generated, nil)
	}
	return c
}
