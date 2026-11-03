// Package transcode carries OpenAI HTTP traffic over vLLM and SGLang gRPC and relays gRPC streams as SSE
package transcode

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A failed call holding the HTTP status and the OpenAI error body to send the client
type Error struct {
	Status int
	Body   []byte
}

// Reports the HTTP status and the error body
func (e *Error) Error() string {
	return fmt.Sprintf("transcode: http %d: %s", e.Status, e.Body)
}

// Sends the error body as an application/json response with the error status
func (e *Error) Write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_, err := w.Write(e.Body)
	return err
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    int     `json:"code"`
}

var errorTypes = map[int]string{
	http.StatusBadRequest:          "BadRequestError",
	http.StatusUnauthorized:        "AuthenticationError",
	http.StatusForbidden:           "PermissionDeniedError",
	http.StatusNotFound:            "NotFoundError",
	http.StatusConflict:            "ConflictError",
	http.StatusUnprocessableEntity: "UnprocessableEntityError",
	http.StatusTooManyRequests:     "RateLimitError",
	StatusClientClosed:             "ClientClosedRequestError",
	http.StatusInternalServerError: "InternalServerError",
	http.StatusNotImplemented:      "NotImplementedError",
	http.StatusBadGateway:          "BadGatewayError",
	http.StatusServiceUnavailable:  "ServiceUnavailableError",
	http.StatusGatewayTimeout:      "GatewayTimeoutError",
}

// The nginx status for a request the client abandoned
const StatusClientClosed = 499

// Builds an error whose body is an OpenAI error object for the status and message
func NewError(code int, msg string) *Error {
	kind, ok := errorTypes[code]
	switch {
	case ok:
	case code >= 500:
		kind = "InternalServerError"
	default:
		kind = "BadRequestError"
	}
	body, err := json.Marshal(errorBody{Error: errorDetail{Message: msg, Type: kind, Code: code}})
	if err != nil {
		panic(err)
	}
	return &Error{Status: code, Body: body}
}

// Builds a 400 error from a formatted message
func BadRequest(format string, args ...any) *Error {
	return NewError(http.StatusBadRequest, fmt.Sprintf(format, args...))
}

// Builds a 502 error from a formatted message describing a malformed backend answer
func BadGateway(format string, args ...any) *Error {
	return NewError(http.StatusBadGateway, fmt.Sprintf(format, args...))
}

// Converts a gRPC or context failure into an error with the matching HTTP status
func FromGRPC(err error) *Error {
	var te *Error
	if errors.As(err, &te) {
		return te
	}
	st, ok := status.FromError(err)
	if !ok {
		st = status.FromContextError(err)
	}
	return NewError(HTTPStatus(st.Code()), st.Message())
}

// Maps a gRPC status code onto the equivalent HTTP status
func HTTPStatus(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.Canceled:
		return StatusClientClosed
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange:
		return http.StatusBadRequest
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted:
		return http.StatusConflict
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Unimplemented:
		return http.StatusNotImplemented
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
