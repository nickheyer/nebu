package vllm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/nickheyer/nebu/pkg/transcode"
	vllmpb "github.com/nickheyer/nebu/pkg/transcode/gen/vllm"
)

// The OpenAI and vLLM max_tokens when a completions request omits it
const defaultCompletionTokens = 16

// An OpenAI legacy completions request with the vLLM extensions the transcoder reads
type completionRequest struct {
	params
	Prompt    json.RawMessage `json:"prompt"`
	Suffix    *string         `json:"suffix"`
	MaxTokens json.RawMessage `json:"max_tokens"`
	Logprobs  *int64          `json:"logprobs"`
	Echo      bool            `json:"echo"`
}

// Decodes one prompt given as text, token ids, or a single element list of either
func parsePrompt(raw json.RawMessage) (string, []uint32, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil, transcode.BadRequest("prompt is required")
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", nil, transcode.BadRequest("prompt: %v", err)
		}
		return text, nil, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", nil, transcode.BadRequest("prompt must be text, token ids, or a list holding one of them")
	}
	if len(list) == 0 {
		return "", nil, transcode.BadRequest("prompt must not be empty")
	}
	switch bytes.TrimSpace(list[0])[0] {
	case '"', '[':
		if len(list) != 1 {
			return "", nil, transcode.BadRequest("a batch of %d prompts is refused, send one prompt per request", len(list))
		}
		raw = list[0]
		if bytes.TrimSpace(raw)[0] == '"' {
			return parsePrompt(raw)
		}
	}
	var ids []uint32
	if err := json.Unmarshal(raw, &ids); err != nil {
		return "", nil, transcode.BadRequest("prompt token ids must be non-negative integers")
	}
	if len(ids) == 0 {
		return "", nil, transcode.BadRequest("prompt must not be empty")
	}
	return "", ids, nil
}

// The prompt text or token ids a completions body carries, refused exactly as Completions would refuse it
func CompletionPrompt(body []byte) (string, []uint32, error) {
	var r completionRequest
	if err := decode(body, &r); err != nil {
		return "", nil, err
	}
	return parsePrompt(r.Prompt)
}

// Sends the prompt text or token ids and answers as an OpenAI text completion or SSE stream
func (c *Client) Completions(ctx context.Context, body []byte, stream bool, call Call) (*transcode.Reply, error) {
	var r completionRequest
	if err := decode(body, &r); err != nil {
		return nil, err
	}
	if r.Suffix != nil && *r.Suffix != "" {
		return nil, transcode.BadRequest("suffix is refused, the vLLM gRPC service has no insertion mode")
	}
	text, ids, err := parsePrompt(r.Prompt)
	if err != nil {
		return nil, err
	}
	rid := call.requestID()
	s := shape{maxTokens: defaultCompletionTokens}
	switch mt := bytes.TrimSpace(r.MaxTokens); {
	case len(mt) == 0:
	case string(mt) == "null":
		s.maxTokens = 0
	default:
		var n int64
		if err := json.Unmarshal(mt, &n); err != nil {
			return nil, transcode.BadRequest("max_tokens must be an integer")
		}
		if s.maxTokens, err = maxTokens("max_tokens", n); err != nil {
			return nil, err
		}
	}
	a := &completion{id: "cmpl-" + rid, created: c.now().Unix(), model: r.Model, echoing: r.Echo, tokenIDs: r.ReturnTokenIDs}
	s.tokenIDs = r.ReturnTokenIDs
	if a.detok, err = call.tokensOut(); err != nil {
		return nil, err
	}
	s.tokensOut = a.detok != nil
	if s.tokensOut {
		a.skip = r.SkipSpecialTokens == nil || *r.SkipSpecialTokens
		if ids == nil {
			if ids, err = call.Tokenizer.Encode(text, true); err != nil {
				return nil, transcode.NewError(http.StatusInternalServerError, fmt.Sprintf("tokenize prompt: %v", err))
			}
			if len(ids) == 0 {
				return nil, transcode.BadRequest("prompt tokenizes to no tokens")
			}
		}
	}
	if r.Logprobs != nil {
		if *r.Logprobs < 0 || *r.Logprobs > math.MaxInt32 {
			return nil, transcode.BadRequest("logprobs must be a non-negative count")
		}
		if a.lp, err = newLogprobs(int(*r.Logprobs), r.ReturnTokensAsTokenIDs, call.Detokenizer); err != nil {
			return nil, err
		}
		s.logprobs, s.topN, s.promptLogprobs = true, uint32(*r.Logprobs), r.Echo
	}
	if r.Echo {
		a.echo = text
		if ids != nil && text == "" {
			if call.Detokenizer == nil {
				return nil, transcode.BadRequest("echo of a token prompt needs a detokenizer for this model")
			}
			if a.echo, err = call.Detokenizer.Decode(ids); err != nil {
				return nil, transcode.NewError(http.StatusInternalServerError, fmt.Sprintf("decode prompt: %v", err))
			}
		}
	}
	if o := r.StreamOptions; o != nil {
		a.includeUsage, a.continuous = o.IncludeUsage, o.IncludeUsage && o.ContinuousUsageStats
	}
	req, err := r.generate(call, rid, stream, s)
	if err != nil {
		return nil, err
	}
	if ids != nil {
		req.Prompt = &vllmpb.GenerateRequest_TokenIds{TokenIds: &vllmpb.TokenIds{Ids: ids}}
	} else {
		req.Prompt = &vllmpb.GenerateRequest_Text{Text: text}
	}
	if stream {
		return c.stream(ctx, req, a)
	}
	return c.unary(ctx, req, a)
}

type completionResponse struct {
	ID               string             `json:"id"`
	Object           string             `json:"object"`
	Created          int64              `json:"created"`
	Model            string             `json:"model"`
	Choices          []completionChoice `json:"choices"`
	Usage            *usage             `json:"usage,omitempty"`
	KVTransferParams map[string]any     `json:"kv_transfer_params,omitempty"`
}

type completionChoice struct {
	Index          int                 `json:"index"`
	Text           string              `json:"text"`
	Logprobs       *completionLogprobs `json:"logprobs"`
	FinishReason   *string             `json:"finish_reason"`
	StopReason     json.RawMessage     `json:"stop_reason"`
	PromptTokenIDs []uint32            `json:"prompt_token_ids,omitempty"`
	TokenIDs       []uint32            `json:"token_ids,omitempty"`
}

// Renders vLLM answers as text_completion bodies and stream chunks
type completion struct {
	id           string
	model        string
	created      int64
	lp           *logprobs
	echoing      bool
	echo         string
	includeUsage bool
	continuous   bool
	started      bool
	prompt       uint32
	generated    uint32
	offset       int
	finish       *vllmpb.FinishInfo
	// Whether choices carry their token ids, and the host side decoding of tokens out mode
	tokenIDs  bool
	detok     StreamDetokenizer
	skip      bool
	tstream   TokenStream
	promptIDs []uint32
}

// The text of a sequence output, decoded by the host from its token ids in tokens out mode
func (a *completion) text(seq *vllmpb.SequenceOutput, whole bool) (string, error) {
	return outputText(a.detok, &a.tstream, a.skip, seq, whole)
}

// The text of a sequence, the engine's own or the host's incremental decode of its ids under skip_special_tokens
func outputText(detok StreamDetokenizer, stream *TokenStream, skip bool, seq *vllmpb.SequenceOutput, whole bool) (string, error) {
	if detok == nil {
		return seq.GetText(), nil
	}
	ids := seq.GetTokenIds()
	if len(ids) == 0 && seq.GetNumTokens() > 0 {
		return "", transcode.BadGateway("backend answer lacks the token ids tokens out mode asked for")
	}
	if whole {
		fresh := detok.Stream(skip)
		stream = &fresh
	} else if *stream == nil {
		*stream = detok.Stream(skip)
	}
	var b strings.Builder
	for _, id := range ids {
		part, err := (*stream).Step(id)
		if err != nil {
			return "", transcode.NewError(http.StatusInternalServerError, fmt.Sprintf("decode token %d: %v", id, err))
		}
		b.WriteString(part)
	}
	if whole || seq.GetFinishInfo() != nil {
		b.WriteString((*stream).Flush())
	}
	return b.String(), nil
}

// Returns an empty legacy logprob object whose lists marshal as arrays
func emptyCompletionLogprobs() *completionLogprobs {
	return &completionLogprobs{TextOffset: []int{}, TokenLogprobs: []*float32{}, Tokens: []string{}, TopLogprobs: []topMap{}}
}

// Appends the echoed prompt token logprobs, the first prompt token carrying no score
func (a *completion) promptLogprobs(out *completionLogprobs, pi *vllmpb.PromptInfo) error {
	if pi == nil {
		return transcode.BadGateway("backend answer lacks prompt info for echoed logprobs")
	}
	ps, err := positions(pi.GetTokenIds(), pi.GetLogprobs(), pi.GetRanks(), pi.GetCandidateTokens())
	if err != nil {
		return err
	}
	return a.lp.completion(out, &a.offset, ps, true)
}

// Appends the generated token logprobs of one sequence output
func (a *completion) outputLogprobs(out *completionLogprobs, seq *vllmpb.SequenceOutput) error {
	ps, err := positions(seq.GetTokenIds(), seq.GetLogprobs(), seq.GetRanks(), seq.GetCandidateTokens())
	if err != nil {
		return err
	}
	return a.lp.completion(out, &a.offset, ps, false)
}

// Builds the text_completion body with echoed prompt, text, logprobs, finish and usage
func (a *completion) whole(resp *vllmpb.GenerateResponse) ([]byte, error) {
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
	choice := completionChoice{Index: int(seq.GetIndex()), Text: a.echo + text, FinishReason: &reason, StopReason: stop}
	if a.tokenIDs {
		choice.PromptTokenIDs, choice.TokenIDs = resp.GetPromptInfo().GetTokenIds(), seq.GetTokenIds()
	}
	if a.lp != nil {
		lps := emptyCompletionLogprobs()
		if a.echoing {
			if err := a.promptLogprobs(lps, resp.GetPromptInfo()); err != nil {
				return nil, err
			}
		}
		if err := a.outputLogprobs(lps, seq); err != nil {
			return nil, err
		}
		choice.Logprobs = lps
	}
	return json.Marshal(completionResponse{
		ID:               a.id,
		Object:           "text_completion",
		Created:          a.created,
		Model:            a.model,
		Choices:          []completionChoice{choice},
		Usage:            newUsage(resp.GetPromptInfo().GetNumPromptTokens(), fi.GetNumOutputTokens(), fi),
		KVTransferParams: kvParams(fi),
	})
}

// Emits the echoed prompt first when asked, then text and logprob chunks with finish_reason on the last
func (a *completion) next(msg *vllmpb.GenerateResponse) ([][]byte, bool, error) {
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
		if a.echoing {
			choice := completionChoice{Text: a.echo, PromptTokenIDs: a.promptIDs}
			a.promptIDs = nil
			if a.lp != nil {
				choice.Logprobs = emptyCompletionLogprobs()
				if err := a.promptLogprobs(choice.Logprobs, pi); err != nil {
					return nil, false, err
				}
			}
			out = append(out, a.chunk(choice))
		}
	}
	if seq := msg.GetOutputs(); seq != nil {
		a.generated += seq.GetNumTokens()
		fi := seq.GetFinishInfo()
		text, err := a.text(seq, false)
		if err != nil {
			return nil, false, err
		}
		choice := completionChoice{Index: int(seq.GetIndex()), Text: text, PromptTokenIDs: a.promptIDs}
		a.promptIDs = nil
		if a.tokenIDs {
			choice.TokenIDs = seq.GetTokenIds()
		}
		if a.lp != nil && len(seq.GetTokenIds()) > 0 {
			choice.Logprobs = emptyCompletionLogprobs()
			if err := a.outputLogprobs(choice.Logprobs, seq); err != nil {
				return nil, false, err
			}
		}
		if fi != nil {
			reason, stop, err := finishReason(fi)
			if err != nil {
				return nil, false, err
			}
			choice.FinishReason, choice.StopReason = &reason, stop
			a.finish = fi
		}
		if choice.Text != "" || choice.Logprobs != nil || fi != nil || len(choice.TokenIDs) > 0 || choice.PromptTokenIDs != nil {
			out = append(out, a.chunk(choice))
		}
	}
	p, err := payloads(out...)
	return p, a.finish != nil, err
}

// Returns the final usage chunk when stream_options asked for it
func (a *completion) tail() ([][]byte, error) {
	if !a.includeUsage {
		return nil, nil
	}
	return payloads(completionResponse{
		ID:      a.id,
		Object:  "text_completion",
		Created: a.created,
		Model:   a.model,
		Choices: []completionChoice{},
		Usage:   newUsage(a.prompt, a.finish.GetNumOutputTokens(), a.finish),
	})
}

// Wraps one choice in a chunk carrying running usage when continuous usage stats are on
func (a *completion) chunk(choice completionChoice) completionResponse {
	c := completionResponse{ID: a.id, Object: "text_completion", Created: a.created, Model: a.model, Choices: []completionChoice{choice}}
	if a.continuous {
		c.Usage = newUsage(a.prompt, a.generated, nil)
	}
	return c
}
