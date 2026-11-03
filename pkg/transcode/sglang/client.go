// Package sglang relays OpenAI JSON through the SGLang runtime gRPC service and reads backend state
package sglang

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"google.golang.org/grpc"

	"github.com/nickheyer/nebu/pkg/transcode"
	runtimev1 "github.com/nickheyer/nebu/pkg/transcode/gen/sglang/runtime/v1"
)

// Reported when a backend health check answers unhealthy
var ErrUnhealthy = errors.New("sglang: backend reports unhealthy")

// Relays OpenAI JSON through the SGLang runtime gRPC service and reads its load and identity
type Client struct {
	api runtimev1.SglangServiceClient
}

// Builds a client over a connection to an SGLang gRPC server
func New(conn grpc.ClientConnInterface) *Client {
	return &Client{api: runtimev1.NewSglangServiceClient(conn)}
}

// One served model card whose parent names the base model of an adapter
type Model struct {
	ID          string
	Root        string
	Parent      string
	MaxModelLen int32
}

// Starts one OpenAI pass-through streaming RPC
type openStream func(ctx context.Context, in *runtimev1.OpenAIRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[runtimev1.OpenAIStreamChunk], error)

// Passes an OpenAI chat body through ChatComplete and answers as JSON or SSE
func (c *Client) ChatCompletions(ctx context.Context, body []byte, stream bool, trace map[string]string) (*transcode.Reply, error) {
	return relay(ctx, c.api.ChatComplete, body, stream, trace)
}

// Passes an OpenAI completions body through Complete and answers as JSON or SSE
func (c *Client) Completions(ctx context.Context, body []byte, stream bool, trace map[string]string) (*transcode.Reply, error) {
	return relay(ctx, c.api.Complete, body, stream, trace)
}

// Passes an OpenAI embeddings body through OpenAIEmbed and answers with its JSON
func (c *Client) Embeddings(ctx context.Context, body []byte, trace map[string]string) (*transcode.Reply, error) {
	resp, err := c.api.OpenAIEmbed(ctx, &runtimev1.OpenAIRequest{JsonBody: body, TraceHeaders: trace})
	if err != nil {
		return nil, transcode.FromGRPC(err)
	}
	if resp.GetStatusCode() != http.StatusOK {
		return nil, statusError(int(resp.GetStatusCode()), resp.GetJsonBody())
	}
	return &transcode.Reply{Body: resp.GetJsonBody()}, nil
}

// Runs a pass-through stream, collecting the single finished chunk for unary calls or relaying chunks as SSE
func relay(ctx context.Context, open openStream, body []byte, stream bool, trace map[string]string) (*transcode.Reply, error) {
	ctx, cancel := context.WithCancel(ctx)
	st, err := open(ctx, &runtimev1.OpenAIRequest{JsonBody: body, TraceHeaders: trace})
	if err != nil {
		cancel()
		return nil, transcode.FromGRPC(err)
	}
	first, err := recv(st)
	if err != nil {
		cancel()
		return nil, err
	}
	if !stream {
		defer cancel()
		if !first.GetFinished() {
			return nil, transcode.BadGateway("backend streamed chunks for a non-streaming request")
		}
		if err := chunkError(first); err != nil {
			return nil, err
		}
		return &transcode.Reply{Body: first.GetJsonChunk()}, nil
	}
	if first.GetFinished() {
		if err := chunkError(first); err != nil {
			cancel()
			return nil, err
		}
	}
	events := func(yield func([]byte, error) bool) {
		defer cancel()
		chunk := first
		for {
			if p := chunk.GetJsonChunk(); len(p) > 0 && !yield(p, payloadError(p)) {
				return
			}
			if chunk.GetFinished() {
				yield(transcode.Done, nil)
				return
			}
			var err error
			if chunk, err = recv(st); err == nil && chunk.GetFinished() {
				err = chunkError(chunk)
			}
			if err != nil {
				te := transcode.FromGRPC(err)
				if yield(te.Body, te) {
					yield(transcode.Done, nil)
				}
				return
			}
		}
	}
	return &transcode.Reply{Events: events}, nil
}

// Receives one chunk, mapping a stream that ends without a finished chunk to a bad gateway error
func recv(st grpc.ServerStreamingClient[runtimev1.OpenAIStreamChunk]) (*runtimev1.OpenAIStreamChunk, error) {
	chunk, err := st.Recv()
	if errors.Is(err, io.EOF) {
		return nil, transcode.BadGateway("backend stream ended without a finished chunk")
	}
	if err != nil {
		return nil, transcode.FromGRPC(err)
	}
	return chunk, nil
}

// Returns the failure an in-stream OpenAI error event reports, nil for ordinary payloads
func payloadError(p []byte) error {
	trimmed := bytes.TrimLeft(p, " \t\r\n")
	if !bytes.HasPrefix(trimmed, []byte("{")) || !bytes.HasPrefix(bytes.TrimLeft(trimmed[1:], " \t\r\n"), []byte(`"error"`)) {
		return nil
	}
	var e struct {
		Error struct {
			Code json.RawMessage `json:"code"`
		} `json:"error"`
	}
	code, n := http.StatusInternalServerError, 0
	if json.Unmarshal(p, &e) == nil && json.Unmarshal(e.Error.Code, &n) == nil && n >= 400 && n <= 599 {
		code = n
	}
	return &transcode.Error{Status: code, Body: p}
}

// Returns the HTTP error a finished chunk reports through a status code other than 200
func chunkError(chunk *runtimev1.OpenAIStreamChunk) error {
	if chunk.StatusCode == nil || chunk.GetStatusCode() == http.StatusOK {
		return nil
	}
	return statusError(int(chunk.GetStatusCode()), chunk.GetJsonChunk())
}

// Builds an error from a backend status and body, wrapping bodies that are not JSON into an OpenAI error
func statusError(code int, body []byte) *transcode.Error {
	if code < 400 || code > 599 {
		return transcode.BadGateway("backend answered with status %d", code)
	}
	if !json.Valid(body) {
		return transcode.NewError(code, string(body))
	}
	return &transcode.Error{Status: code, Body: body}
}

// Asks the backend for its health and returns ErrUnhealthy when it reports unhealthy
func (c *Client) HealthCheck(ctx context.Context) error {
	resp, err := c.api.HealthCheck(ctx, &runtimev1.HealthCheckRequest{})
	if err != nil {
		return transcode.FromGRPC(err)
	}
	if !resp.GetHealthy() {
		return ErrUnhealthy
	}
	return nil
}

// Returns the scheduler load JSON for one data parallel rank, or every rank when rank is nil
func (c *Client) GetLoad(ctx context.Context, rank *int32) (json.RawMessage, error) {
	resp, err := c.api.GetLoad(ctx, &runtimev1.GetLoadRequest{DpRank: rank})
	if err != nil {
		return nil, transcode.FromGRPC(err)
	}
	return info("load", resp.GetJsonInfo())
}

// Returns the server configuration and runtime state JSON
func (c *Client) GetServerInfo(ctx context.Context) (json.RawMessage, error) {
	resp, err := c.api.GetServerInfo(ctx, &runtimev1.GetServerInfoRequest{})
	if err != nil {
		return nil, transcode.FromGRPC(err)
	}
	return info("server info", resp.GetJsonInfo())
}

// Returns JSON info text as raw JSON, failing when the backend sent invalid JSON
func info(what, text string) (json.RawMessage, error) {
	if !json.Valid([]byte(text)) {
		return nil, transcode.BadGateway("backend %s is not valid JSON", what)
	}
	return json.RawMessage(text), nil
}

// Returns the served model cards including adapters
func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	resp, err := c.api.ListModels(ctx, &runtimev1.ListModelsRequest{})
	if err != nil {
		return nil, transcode.FromGRPC(err)
	}
	out := make([]Model, 0, len(resp.GetModels()))
	for _, m := range resp.GetModels() {
		out = append(out, Model{ID: m.GetId(), Root: m.GetRoot(), Parent: m.GetParent(), MaxModelLen: m.GetMaxModelLen()})
	}
	return out, nil
}
