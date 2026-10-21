package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Error codes answers carry, in the words clients match on
const (
	CodeInvalidRequest   = "invalid_request_error"
	CodeAuthentication   = "authentication_error"
	CodePermission       = "permission_error"
	CodeNotFound         = "not_found_error"
	CodeTooLarge         = "request_too_large"
	CodeRateLimit        = "rate_limit_error"
	CodeOverloaded       = "overloaded_error"
	CodeAPI              = "api_error"
	CodeTimeout          = "timeout_error"
	CodeServer           = "server_error"
	CodeUpstream         = "upstream_error"
	CodeModelNotFound    = "model_not_found"
	CodeModelStarting    = "model_starting"
	CodeModelSwapping    = "model_swapping"
	CodeModelUnavailable = "model_unavailable"
)

// Why a request was not answered, with the status and code the client reads
type Failure struct {
	Status  int
	Code    string
	Message string
}

func (f *Failure) Error() string { return f.Message }

// A refusal of the request itself
func bad(format string, args ...any) error {
	return &Failure{Status: http.StatusBadRequest, Code: CodeInvalidRequest, Message: "bad request: " + fmt.Sprintf(format, args...)}
}

// The failure an error stands for, a plain error being a refusal of the request
func FailureOf(err error) Failure {
	if f, ok := err.(*Failure); ok {
		return *f
	}
	return Failure{Status: http.StatusBadRequest, Code: CodeInvalidRequest, Message: err.Error()}
}

// A model answered a tool call with arguments that are not a JSON object
type ToolArgsError struct{ Name string }

func (e *ToolArgsError) Error() string {
	return "model answered tool call " + e.Name + " with arguments that are not a JSON object"
}

// The message of an error object with a message field
func errorField(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(body, &e)
	return e.Error.Message
}

// The error type a body names, or the one that fits the status
func CodeOf(status int, body []byte) string {
	var e struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Type != "" {
		return e.Error.Type
	}
	switch status {
	case http.StatusBadRequest:
		return CodeInvalidRequest
	case http.StatusUnauthorized:
		return CodeAuthentication
	case http.StatusForbidden:
		return CodePermission
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusRequestEntityTooLarge:
		return CodeTooLarge
	case http.StatusTooManyRequests:
		return CodeRateLimit
	case 529:
		return CodeOverloaded
	}
	return CodeAPI
}

// A runtime's refusal in its own words, the body when it names no message
func Refusal(rt Runtime, status int, body []byte) *Failure {
	message := rt.ErrorMessage(body)
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	return &Failure{Status: status, Code: CodeOf(status, body), Message: message}
}
