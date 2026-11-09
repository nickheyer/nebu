// Package vllm transcodes OpenAI chat and completions requests to the vLLM Inference gRPC service
package vllm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"google.golang.org/grpc"

	"github.com/nickheyer/nebu/pkg/transcode"
	vllmpb "github.com/nickheyer/nebu/pkg/transcode/gen/vllm"
)

// Transcodes OpenAI HTTP requests into calls on the vLLM Inference gRPC service
type Client struct {
	api vllmpb.InferenceClient
	now func() time.Time
}

// Builds a client over a connection to a vLLM gRPC server
func New(conn grpc.ClientConnInterface) *Client {
	return &Client{api: vllmpb.NewInferenceClient(conn), now: time.Now}
}

// Turns backend responses into OpenAI JSON for one endpoint
type answer interface {
	// Builds the complete response body from a unary backend answer
	whole(resp *vllmpb.GenerateResponse) ([]byte, error)
	// Returns the SSE payloads for one streamed backend message and whether the sequence finished
	next(msg *vllmpb.GenerateResponse) ([][]byte, bool, error)
	// Returns the SSE payloads sent after the sequence finished and before the done marker
	tail() ([][]byte, error)
}

// Runs a unary Generate call and renders the full response body
func (c *Client) unary(ctx context.Context, req *vllmpb.GenerateRequest, a answer) (*transcode.Reply, error) {
	resp, err := c.api.Generate(ctx, req)
	if err != nil {
		return nil, transcode.FromGRPC(err)
	}
	body, err := a.whole(resp)
	if err != nil {
		return nil, transcode.FromGRPC(err)
	}
	return &transcode.Reply{Body: body}, nil
}

// Opens GenerateStream, waits for the first message so early failures map to HTTP errors, then relays SSE payloads
func (c *Client) stream(ctx context.Context, req *vllmpb.GenerateRequest, a answer) (*transcode.Reply, error) {
	ctx, cancel := context.WithCancel(ctx)
	st, err := c.api.GenerateStream(ctx, req)
	if err != nil {
		cancel()
		return nil, transcode.FromGRPC(err)
	}
	first, err := st.Recv()
	if err != nil {
		cancel()
		if errors.Is(err, io.EOF) {
			return nil, transcode.BadGateway("backend closed the stream before answering")
		}
		return nil, transcode.FromGRPC(err)
	}
	events := func(yield func([]byte, error) bool) {
		defer cancel()
		fail := func(err error) {
			te := transcode.FromGRPC(err)
			if yield(te.Body, te) {
				yield(transcode.Done, nil)
			}
		}
		send := func(payloads [][]byte) bool {
			for _, p := range payloads {
				if !yield(p, nil) {
					return false
				}
			}
			return true
		}
		msg := first
		for {
			payloads, finished, err := a.next(msg)
			if err != nil {
				fail(err)
				return
			}
			if !send(payloads) {
				return
			}
			if finished {
				break
			}
			if msg, err = st.Recv(); err != nil {
				if errors.Is(err, io.EOF) {
					err = transcode.BadGateway("backend stream ended before the sequence finished")
				}
				fail(err)
				return
			}
		}
		payloads, err := a.tail()
		if err != nil {
			fail(err)
			return
		}
		if send(payloads) {
			yield(transcode.Done, nil)
		}
	}
	return &transcode.Reply{Events: events}, nil
}

// The OpenAI token accounting object
type usage struct {
	PromptTokens     uint32         `json:"prompt_tokens"`
	CompletionTokens uint32         `json:"completion_tokens"`
	TotalTokens      uint32         `json:"total_tokens"`
	PromptDetails    *promptDetails `json:"prompt_tokens_details,omitempty"`
}

type promptDetails struct {
	CachedTokens uint32 `json:"cached_tokens"`
}

// Builds usage from prompt and completion counts and the cached prompt count when the backend reports it
func newUsage(prompt, completion uint32, fi *vllmpb.FinishInfo) *usage {
	u := &usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion}
	if fi != nil && fi.NumCachedTokens != nil {
		u.PromptDetails = &promptDetails{CachedTokens: fi.GetNumCachedTokens()}
	}
	return u
}

// Maps finish info onto the OpenAI finish_reason and the vLLM stop_reason JSON value
func finishReason(fi *vllmpb.FinishInfo) (string, json.RawMessage, error) {
	null := json.RawMessage("null")
	switch fi.GetFinishReason() {
	case vllmpb.FinishInfo_LENGTH:
		return "length", null, nil
	case vllmpb.FinishInfo_ABORTED:
		return "abort", null, nil
	case vllmpb.FinishInfo_STOP:
		switch r := fi.GetStopReason().(type) {
		case *vllmpb.FinishInfo_StopTokenId:
			return "stop", json.RawMessage(strconv.FormatUint(uint64(r.StopTokenId), 10)), nil
		case *vllmpb.FinishInfo_StopString:
			b, err := json.Marshal(r.StopString)
			return "stop", b, err
		}
		return "stop", null, nil
	}
	return "", nil, transcode.BadGateway("backend finished a sequence with reason %s", fi.GetFinishReason())
}

// Returns kv_transfer_params from finish info as a JSON object map, nil when absent
func kvParams(fi *vllmpb.FinishInfo) map[string]any {
	if fi.GetKvTransferParams() == nil {
		return nil
	}
	return fi.GetKvTransferParams().AsMap()
}

// Marshals each value into an SSE payload
func payloads(values ...any) ([][]byte, error) {
	out := make([][]byte, 0, len(values))
	for _, v := range values {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}
