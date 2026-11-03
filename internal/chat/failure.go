package chat

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
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
	// How long the client should wait before trying again, zero when nothing names a wait
	RetryAfter time.Duration
}

func (f *Failure) Error() string { return f.Message }

// A refusal of the request itself
func bad(format string, args ...any) error {
	return &Failure{Status: http.StatusBadRequest, Code: CodeInvalidRequest, Message: "bad request: " + fmt.Sprintf(format, args...)}
}

// A runtime answer that does not read as its protocol says it should
func malformed(err error) error {
	return &Failure{Status: http.StatusBadGateway, Code: CodeUpstream, Message: "the runtime answered with a body that does not parse: " + err.Error()}
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

// The message and type of an error object under an error key
func errorObject(body []byte) (message, kind string) {
	decode(body, func(s *scan) error {
		return s.object(func(key []byte) error {
			if string(key) != "error" || s.peek() != '{' {
				return s.skip()
			}
			return s.object(func(key []byte) error {
				var err error
				switch string(key) {
				case "message":
					message, err = s.str()
				case "type":
					kind, err = s.str()
				default:
					err = s.skip()
				}
				return err
			})
		})
	})
	return message, kind
}

// The message of an error object with a message field
func errorField(body []byte) string {
	message, _ := errorObject(body)
	return message
}

// The error type a body names, or the one that fits the status
func CodeOf(status int, body []byte) string {
	if _, kind := errorObject(body); kind != "" {
		return kind
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

// A runtime's refusal in its own words, the body when it names none, with the wait its headers ask
func Refusal(rt Runtime, status int, header http.Header, body []byte) *Failure {
	message := rt.ErrorMessage(body)
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	return &Failure{Status: status, Code: CodeOf(status, body), Message: message, RetryAfter: RetryAfterOf(header, time.Now())}
}

// The wait headers ask for from retry-after-ms or Retry-After in seconds or as a date, zero for none
func RetryAfterOf(header http.Header, now time.Time) time.Duration {
	if ms := strings.TrimSpace(header.Get("retry-after-ms")); ms != "" {
		if v, err := strconv.ParseFloat(ms, 64); err == nil && v > 0 && !math.IsInf(v, 0) {
			return time.Duration(v * float64(time.Millisecond))
		}
	}
	after := strings.TrimSpace(header.Get("Retry-After"))
	if after == "" {
		return 0
	}
	if v, err := strconv.ParseFloat(after, 64); err == nil {
		if v <= 0 || math.IsInf(v, 0) || math.IsNaN(v) {
			return 0
		}
		return time.Duration(v * float64(time.Second))
	}
	if at, err := http.ParseTime(after); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

// Sets Retry-After in whole seconds rounded up when the failure names a wait
func retryHeader(w http.ResponseWriter, f Failure) {
	if f.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(int64(math.Ceil(f.RetryAfter.Seconds())), 10))
	}
}
