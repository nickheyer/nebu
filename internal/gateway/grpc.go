package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"iter"
	"net/http"
	"strings"
	"sync"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/telemetry"
	"github.com/nickheyer/nebu/pkg/transcode"
	"github.com/nickheyer/nebu/pkg/transcode/sglang"
	"github.com/nickheyer/nebu/pkg/transcode/vllm"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// Runtimes whose endpoints the gateway reaches over gRPC
const (
	runtimeVLLM   = "vllm"
	runtimeSGLang = "sglang"
)

// One shared gRPC connection per endpoint address, opened through the dialer when one is set
type conns struct {
	m    sync.Map
	dial Dialer
}

func newConns(dial Dialer) *conns { return &conns{dial: dial} }

// The connection to an address, dialed once and kept for every later call
func (c *conns) get(addr string) (*grpc.ClientConn, error) {
	if v, ok := c.m.Load(addr); ok {
		return v.(*grpc.ClientConn), nil
	}
	conn, err := transcode.DialWith(addr, transcode.GRPC, c.dial)
	if err != nil {
		return nil, err
	}
	if v, loaded := c.m.LoadOrStore(addr, conn); loaded {
		conn.Close()
		return v.(*grpc.ClientConn), nil
	}
	return conn, nil
}

// Closes every connection
func (c *conns) close() {
	c.m.Range(func(k, v any) bool {
		v.(*grpc.ClientConn).Close()
		c.m.Delete(k)
		return true
	})
}

// Sends an OpenAI body to a vLLM or SGLang endpoint over gRPC in the caller's trace and answers its reply as HTTP
func (x *exchange) sendGRPC(ctx context.Context, ep datalayer.Endpoint, out *upstream) (*http.Response, error) {
	conn, err := x.g.conns.get(ep.Address().String())
	if err != nil {
		return nil, err
	}
	carrier := http.Header{}
	telemetry.Inject(ctx, carrier)
	if id := out.header.Get(requestcontrol.HeaderRequestID); id != "" {
		carrier.Set(requestcontrol.HeaderRequestID, id)
	}
	trace := make(map[string]string, len(carrier))
	for k, v := range carrier {
		trace[strings.ToLower(k)] = strings.Join(v, ",")
	}
	ctx = metadata.NewOutgoingContext(ctx, metadata.New(trace))
	var reply *transcode.Reply
	switch x.pool.Runtime {
	case runtimeVLLM:
		reply, err = x.vllmCall(ctx, vllm.New(conn), ep, out)
	case runtimeSGLang:
		client := sglang.New(conn)
		switch out.path {
		case chat.OpenAIChat:
			reply, err = client.ChatCompletions(ctx, out.body, out.stream, trace)
		case chat.OpenAICompletions:
			reply, err = client.Completions(ctx, out.body, out.stream, trace)
		case chat.OpenAIEmbeddings:
			reply, err = client.Embeddings(ctx, out.body, trace)
		default:
			err = transcode.NewError(http.StatusNotFound, "SGLang serves "+out.path+" over HTTP alone, and pool "+string(x.pool.ID)+" speaks gRPC")
		}
	default:
		err = transcode.NewError(http.StatusBadRequest, "runtime "+x.pool.Runtime+" has no gRPC service the gateway transcodes to")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return replyResponse(reply, err)
}

// Calls the vLLM Inference service, rendering chats with the instance's chat template and its template arguments
func (x *exchange) vllmCall(ctx context.Context, client *vllm.Client, ep datalayer.Endpoint, out *upstream) (*transcode.Reply, error) {
	call := vllm.Call{RequestID: x.requestID()}
	if x.dec != nil {
		call.Priority, call.Adapter = int32(-x.dec.Request.Objective.Priority), x.dec.Adapter
	}
	if err := x.tokensInOutCall(&call, ep); err != nil {
		return nil, err
	}
	switch out.path {
	case chat.OpenAIChat:
		tpl, err := x.g.templates.Get(ep.Labels()[datalayer.LabelInstance])
		if err != nil {
			return nil, transcode.NewError(http.StatusInternalServerError, "no chat template for "+ep.Address().String()+": "+err.Error())
		}
		body, kwargs, err := templateKwargs(out.body, out.doc)
		if err != nil {
			return nil, transcode.BadRequest("chat_template_kwargs: %v", err)
		}
		return client.ChatCompletions(ctx, body, tpl.With(kwargs), out.stream, call)
	case chat.OpenAICompletions:
		return client.Completions(ctx, out.body, out.stream, call)
	}
	return nil, transcode.NewError(http.StatusNotFound, "vLLM serves "+out.path+" over HTTP alone, and pool "+string(x.pool.ID)+" speaks gRPC")
}

// Takes chat_template_kwargs out of a chat body for the renderer through the body's document, leaving the body the transcoder reads
func templateKwargs(body []byte, doc bodyparse.Document) ([]byte, map[string]any, error) {
	if doc == nil {
		if !bytes.Contains(body, []byte(`"chat_template_kwargs"`)) {
			return body, nil, nil
		}
		var err error
		if doc, err = parseDoc(chat.OpenAIChat, body); err != nil {
			return nil, nil, err
		}
	}
	raw, ok := doc.Field("chat_template_kwargs")
	if !ok || string(raw) == "null" {
		return doc.Bytes(), nil, nil
	}
	var kwargs map[string]any
	if err := json.Unmarshal(raw, &kwargs); err != nil {
		return nil, nil, err
	}
	stripped, err := doc.WithField("chat_template_kwargs", []byte("null"))
	if err != nil {
		return nil, nil, err
	}
	return stripped.Bytes(), kwargs, nil
}

// A transcoder reply as an HTTP response, its failures carried as their status and OpenAI error body
func replyResponse(reply *transcode.Reply, err error) (*http.Response, error) {
	if err != nil {
		te, ok := err.(*transcode.Error)
		if !ok {
			te = transcode.FromGRPC(err)
		}
		return &http.Response{StatusCode: te.Status, Status: http.StatusText(te.Status), Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(te.Body))}, nil
	}
	if reply.Events == nil {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(reply.Body))}, nil
	}
	next, stop := iter.Pull2(reply.Events)
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &sseReader{next: next, stop: stop}}, nil
}

// A backend failure whose error payload the stream already carried to the client
type relayedFailure struct{ err error }

func (e *relayedFailure) Error() string { return e.err.Error() }

func (e *relayedFailure) Unwrap() error { return e.err }

// Reads transcoded payloads as server sent events, a failing payload delivered and then its error answered in place of the end
type sseReader struct {
	next func() ([]byte, error, bool)
	stop func()
	buf  []byte
	err  error
}

func (s *sseReader) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		if s.err != nil {
			return 0, s.err
		}
		payload, err, ok := s.next()
		if !ok {
			return 0, io.EOF
		}
		if err != nil {
			s.err = &relayedFailure{err: err}
		}
		s.buf = append(append(append(s.buf[:0], "data: "...), payload...), "\n\n"...)
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *sseReader) Close() error {
	s.stop()
	return nil
}
