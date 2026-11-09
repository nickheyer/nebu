package requestcontrol

import (
	"context"
	"errors"
	"net/http"

	"github.com/nickheyer/nebu/pkg/bodyparse"
)

// Status a client closed request is logged with, as no response reaches the client
const StatusClientClosed = 499

// Seconds a refused client is told to wait before retrying
const (
	retryShed        = "1"
	retryUnavailable = "1"
)

// The HTTP status and Retry-After value a director error answers with
func Status(err error) (int, string) {
	switch {
	case err == nil:
		return http.StatusOK, ""
	case errors.Is(err, ErrGuardrailBlocked):
		if be, ok := errors.AsType[*BlockedError](err); ok && be.Status == http.StatusBadRequest {
			return http.StatusBadRequest, ""
		}
		return http.StatusForbidden, ""
	case errors.Is(err, ErrShed):
		return http.StatusTooManyRequests, retryShed
	case errors.Is(err, ErrNoReadyEndpoints), errors.Is(err, ErrSchedulerUnavailable):
		return http.StatusServiceUnavailable, retryUnavailable
	case errors.Is(err, ErrUnknownModel):
		return http.StatusNotFound, ""
	case errors.Is(err, bodyparse.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, ""
	case errors.Is(err, ErrNoModel), errors.Is(err, ErrInvalidHeader), errors.Is(err, bodyparse.ErrNotJSON), errors.Is(err, bodyparse.ErrNoModel):
		return http.StatusBadRequest, ""
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, ""
	case errors.Is(err, context.Canceled):
		return StatusClientClosed, ""
	}
	return http.StatusInternalServerError, ""
}
