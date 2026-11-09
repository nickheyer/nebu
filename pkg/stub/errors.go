package stub

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// An OpenAI shaped error and the status it is sent with
type apiError struct {
	status  int
	message string
	kind    string
	param   string
	code    string
}

// JSON envelope OpenAI clients parse from a failed request
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    *string `json:"code"`
}

// JSON envelope with empty param and code sent as null
func (e *apiError) body() errorBody {
	d := errorDetail{Message: e.message, Type: e.kind}
	if e.param != "" {
		d.Param = &e.param
	}
	if e.code != "" {
		d.Code = &e.code
	}
	return errorBody{Error: d}
}

// Sends the failure status with its OpenAI JSON envelope
func writeError(w http.ResponseWriter, e *apiError) { writeJSON(w, e.status, e.body()) }

// Sends a status with a value encoded as JSON
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Sends a status with a plain text body
func writeText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	w.Write([]byte(text))
}

// A 400 invalid request error naming the offending parameter
func badRequest(param, format string, args ...any) *apiError {
	return &apiError{status: http.StatusBadRequest, message: fmt.Sprintf(format, args...), kind: "invalid_request_error", param: param}
}

// A 404 for a model that is neither the base model nor a known adapter
func errModelNotFound(model string) *apiError {
	return &apiError{status: http.StatusNotFound, message: fmt.Sprintf("The model `%s` does not exist.", model), kind: "invalid_request_error", param: "model", code: "model_not_found"}
}

// A 404 for an adapter the stub does not know
func errAdapterNotFound(name string) *apiError {
	return &apiError{status: http.StatusNotFound, message: fmt.Sprintf("The lora adapter '%s' cannot be found.", name), kind: "invalid_request_error", param: "lora_name", code: "lora_not_found"}
}

// A 409 for a load whose adapter was unloaded before it became resident
func errAdapterUnloaded(name string) *apiError {
	return &apiError{status: http.StatusConflict, message: fmt.Sprintf("The lora adapter '%s' was unloaded before it finished loading.", name), kind: "invalid_request_error", param: "lora_name", code: "lora_unloaded"}
}

// A 429 for a queue deeper than the reject threshold
func errQueueFull(depth, limit int) *apiError {
	return &apiError{status: http.StatusTooManyRequests, message: fmt.Sprintf("Queue depth %d is above %d.", depth, limit), kind: "requests", code: "rate_limit_exceeded"}
}

// A 500 from the fail rate fault
func errInjected() *apiError {
	return &apiError{status: http.StatusInternalServerError, message: "Injected failure.", kind: "server_error", code: "injected_failure"}
}

// A 503 while the model is loading
func errLoading() *apiError {
	return &apiError{status: http.StatusServiceUnavailable, message: "Loading model.", kind: "unavailable_error", code: "model_loading"}
}

// A 503 for requests pending when the stub closes
func errShutdown() *apiError {
	return &apiError{status: http.StatusServiceUnavailable, message: "Server is shutting down.", kind: "unavailable_error", code: "shutting_down"}
}
