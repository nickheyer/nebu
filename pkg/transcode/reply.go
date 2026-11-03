package transcode

import (
	"iter"
	"net/http"
)

// Payload of the final SSE event of an OpenAI stream
var Done = []byte("[DONE]")

// A unary JSON body or SSE payloads where a payload paired with an error reports a backend failure
type Reply struct {
	Body   []byte
	Events iter.Seq2[[]byte, error]
}

// Sends the body as JSON or relays every event over SSE and returns the first backend failure seen
func (r *Reply) Write(w http.ResponseWriter) error {
	if r.Events == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write(r.Body)
		return err
	}
	sse, err := NewSSEWriter(w)
	if err != nil {
		return err
	}
	defer sse.Close()
	var failure error
	for payload, ferr := range r.Events {
		if err := sse.Data(payload); err != nil {
			return err
		}
		if ferr != nil && failure == nil {
			failure = ferr
		}
	}
	return failure
}
