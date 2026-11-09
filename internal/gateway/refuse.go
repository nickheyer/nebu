package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/transcode"
)

// Bytes a request body may hold, room for inline images without letting one request exhaust memory
const maxBody = 64 << 20

// Status answered when a request crossed more gateways than the mesh allows
const statusLoop = http.StatusLoopDetected

// A request the route policy refused, with the wait its limits ask for
type policyError struct {
	wait time.Duration
	msg  string
}

func (e *policyError) Error() string { return e.msg }

func (e *policyError) Unwrap() error { return requestcontrol.ErrShed }

// A refusal of the request as the client wrote it
func bad(format string, args ...any) error {
	return &chat.Failure{Status: http.StatusBadRequest, Code: chat.CodeInvalidRequest, Message: "bad request: " + fmt.Sprintf(format, args...)}
}

// Reads a bounded body as chunks of at most bodyparse.ChunkBytes, an oversized one failing with bodyparse.ErrTooLarge
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	chunks := bodyparse.NewReader(r.Body, maxBody)
	var body []byte
	if r.ContentLength > 0 && r.ContentLength <= maxBody {
		body = make([]byte, 0, r.ContentLength)
	}
	for {
		chunk, last, err := chunks.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, bodyparse.ErrTooLarge) {
			return nil, fmt.Errorf("%w: request body is over %d bytes", bodyparse.ErrTooLarge, maxBody)
		}
		if err != nil {
			return nil, bad("could not read request body: %v", err)
		}
		body = append(body, chunk...)
		if last {
			break
		}
	}
	return body, nil
}

// Parses the routing fields of a JSON body by the shape its path carries
func parseDoc(path string, body []byte) (bodyparse.Document, error) {
	return bodyparse.Parse(bodyparse.KindOf(path), body)
}

// Whole seconds rounded up, at least one
func seconds(d time.Duration) time.Duration {
	return time.Duration(max(1, math.Ceil(d.Seconds()))) * time.Second
}

// The failure a client gets for an error around dispatch, worded for the model, route and pool
func (g *Gateway) refusal(err error, model string, route *v1.Route, pool datalayer.PoolID) chat.Failure {
	state := route.GetStatus().GetState()
	if errors.Is(err, flowcontrol.ErrQueueTimeout) && pool != "" && len(g.store.Endpoints(pool, datalayer.ReadyAndHealthy)) == 0 {
		err = fmt.Errorf("%w: pool %s gained no ready endpoint while the request waited", requestcontrol.ErrNoReadyEndpoints, pool)
	}
	switch {
	case errors.As(err, new(*chat.Failure)):
		f, _ := errors.AsType[*chat.Failure](err)
		return *f
	case errors.As(err, new(*policyError)):
		pe, _ := errors.AsType[*policyError](err)
		return chat.Failure{Status: http.StatusTooManyRequests, Code: chat.CodeRateLimit, Message: pe.msg, RetryAfter: seconds(pe.wait)}
	case errors.Is(err, requestcontrol.ErrGuardrailBlocked):
		status, _ := requestcontrol.Status(err)
		return chat.Failure{Status: status, Code: chat.CodeInvalidRequest, Message: err.Error()}
	case errors.Is(err, mesh.ErrHopLimit):
		return chat.Failure{Status: statusLoop, Code: chat.CodeInvalidRequest, Message: "model " + model + " was forwarded past the hop limit: " + err.Error()}
	case errors.Is(err, mesh.ErrMesh):
		return chat.Failure{Status: http.StatusBadRequest, Code: chat.CodeInvalidRequest, Message: err.Error()}
	case errors.As(err, new(*flowcontrol.QuotaError)):
		qe, _ := errors.AsType[*flowcontrol.QuotaError](err)
		return chat.Failure{Status: http.StatusTooManyRequests, Code: chat.CodeRateLimit, Message: qe.Error(), RetryAfter: seconds(qe.Retry)}
	case errors.Is(err, requestcontrol.ErrShed):
		return chat.Failure{Status: http.StatusTooManyRequests, Code: chat.CodeRateLimit, Message: "model " + model + " is saturated, retry shortly: " + err.Error(), RetryAfter: g.cfg.Retry.Shed}
	case state == v1.RouteState_ROUTE_STATE_PENDING && (errors.Is(err, requestcontrol.ErrUnknownModel) || errors.Is(err, requestcontrol.ErrNoReadyEndpoints)):
		return chat.Failure{Status: http.StatusServiceUnavailable, Code: chat.CodeModelStarting, Message: "model " + model + " is starting, retry shortly", RetryAfter: g.cfg.Retry.Unavailable}
	case state == v1.RouteState_ROUTE_STATE_DRAINING && (errors.Is(err, requestcontrol.ErrUnknownModel) || errors.Is(err, requestcontrol.ErrNoReadyEndpoints)):
		return chat.Failure{Status: http.StatusServiceUnavailable, Code: chat.CodeModelSwapping, Message: "model " + model + " is being replaced, retry shortly", RetryAfter: g.cfg.Retry.Unavailable}
	case errors.Is(err, requestcontrol.ErrUnknownModel):
		return chat.Failure{Status: http.StatusNotFound, Code: chat.CodeModelNotFound, Message: "model " + model + " is not served, run it with nebu run"}
	case errors.Is(err, requestcontrol.ErrNoReadyEndpoints):
		return chat.Failure{Status: http.StatusServiceUnavailable, Code: chat.CodeModelUnavailable, Message: "model " + model + " has no ready endpoint, retry shortly", RetryAfter: g.cfg.Retry.Unavailable}
	case errors.Is(err, requestcontrol.ErrSchedulerUnavailable):
		return chat.Failure{Status: http.StatusServiceUnavailable, Code: chat.CodeModelUnavailable, Message: "the scheduler for " + model + " is unavailable: " + err.Error(), RetryAfter: g.cfg.Retry.Unavailable}
	case errors.Is(err, requestcontrol.ErrNoModel), errors.Is(err, bodyparse.ErrNoModel):
		return chat.Failure{Status: http.StatusBadRequest, Code: chat.CodeInvalidRequest, Message: "the request names no model and more than one is served"}
	case errors.Is(err, requestcontrol.ErrInvalidHeader), errors.Is(err, bodyparse.ErrNotJSON):
		return chat.Failure{Status: http.StatusBadRequest, Code: chat.CodeInvalidRequest, Message: err.Error()}
	case errors.Is(err, bodyparse.ErrTooLarge):
		return chat.Failure{Status: http.StatusRequestEntityTooLarge, Code: chat.CodeTooLarge, Message: err.Error()}
	}
	return upstreamFailure(model, err)
}

// The failure for an unreachable or failing runtime, timeouts as 504 and the rest as 502
func upstreamFailure(model string, err error) chat.Failure {
	if f, ok := errors.AsType[*chat.Failure](err); ok {
		if f.Status >= http.StatusInternalServerError {
			return chat.Failure{Status: http.StatusBadGateway, Code: chat.CodeUpstream, Message: "upstream: " + f.Message, RetryAfter: f.RetryAfter}
		}
		return *f
	}
	if te, ok := errors.AsType[*transcode.Error](err); ok {
		f := chat.Refusal(chat.RuntimeOf(v1.ApiFlavor_API_FLAVOR_OPENAI), te.Status, http.Header{}, te.Body)
		return upstreamFailure(model, f)
	}
	switch {
	case errors.Is(err, context.Canceled):
		return chat.Failure{Status: requestcontrol.StatusClientClosed, Code: chat.CodeInvalidRequest, Message: "the client went away"}
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		return chat.Failure{Status: http.StatusGatewayTimeout, Code: chat.CodeTimeout, Message: "model " + model + " did not answer within its timeout"}
	}
	return chat.Failure{Status: http.StatusBadGateway, Code: chat.CodeServer, Message: "upstream error: " + err.Error()}
}

// The failure an error stands for when no model or route frames it
func failureOf(err error) chat.Failure {
	if f, ok := errors.AsType[*chat.Failure](err); ok {
		return *f
	}
	if errors.Is(err, bodyparse.ErrTooLarge) {
		return chat.Failure{Status: http.StatusRequestEntityTooLarge, Code: chat.CodeTooLarge, Message: err.Error()}
	}
	return chat.Failure{Status: http.StatusBadRequest, Code: chat.CodeInvalidRequest, Message: err.Error()}
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}
