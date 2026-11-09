package chat

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// A response recorder safe to read while a keepalive goroutine writes to it
type syncRecorder struct {
	mu     sync.Mutex
	header http.Header
	body   bytes.Buffer
}

func (r *syncRecorder) Header() http.Header { return r.header }
func (r *syncRecorder) WriteHeader(int)     {}
func (r *syncRecorder) Flush()              {}

func (r *syncRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(b)
}

func (r *syncRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

// The fields the Ollama Go client decodes a stream line into, as api.ChatResponse and api.GenerateResponse declare them
type ollamaClientLine struct {
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	Message   *struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Response *string `json:"response"`
	Done     bool    `json:"done"`
}

// An Ollama stream silent for the keepalive period gets an empty chunk that is not done, never sooner
func TestOllamaKeepalive(t *testing.T) {
	for _, kind := range []Kind{Chat, Generate} {
		synctest.Test(t, func(t *testing.T) {
			rec := &syncRecorder{header: http.Header{}}
			out := (ollama{}).Answer(rec, &Request{Kind: kind, Stream: true}, "m")
			lines := func() []string { return strings.Split(strings.TrimSpace(rec.String()), "\n") }
			time.Sleep(5 * time.Second)
			if err := out.Write(Event{Kind: Said, Text: "hi"}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(keepaliveEvery - time.Nanosecond)
			synctest.Wait()
			if n := len(lines()); n != 1 {
				t.Fatalf("keepalive before %v of silence: %q", keepaliveEvery, rec.String())
			}
			time.Sleep(time.Nanosecond)
			synctest.Wait()
			got := lines()
			if len(got) != 2 {
				t.Fatalf("no keepalive after %v of silence: %q", keepaliveEvery, rec.String())
			}
			var line ollamaClientLine
			if err := json.Unmarshal([]byte(got[1]), &line); err != nil {
				t.Fatalf("the Ollama client cannot read %s: %v", got[1], err)
			}
			if line.Done || line.Model != "m" || line.CreatedAt.IsZero() {
				t.Fatalf("keepalive %s", got[1])
			}
			if kind == Chat && (line.Message == nil || line.Message.Role != "assistant" || line.Message.Content != "") {
				t.Fatalf("chat keepalive %s", got[1])
			}
			if kind == Generate && (line.Response == nil || *line.Response != "") {
				t.Fatalf("generate keepalive %s", got[1])
			}
			time.Sleep(keepaliveEvery)
			synctest.Wait()
			if len(lines()) != 3 {
				t.Fatalf("keepalives stop after the first: %q", rec.String())
			}
			out.Write(Event{Kind: Stopped, Res: &Result{}})
			out.Close()
			last := lines()[3]
			if !strings.Contains(last, `"done":true`) {
				t.Fatalf("stop line %s", last)
			}
		})
	}
}

// The SSE writers keep their own keepalives, a comment for OpenAI and Responses and a ping event for Anthropic
func TestSSEKeepalives(t *testing.T) {
	for _, tc := range []struct {
		client Client
		req    *Request
		ping   string
	}{
		{openai{}, &Request{Kind: Chat, Stream: true}, ": ping\n\n"},
		{openai{}, &Request{Kind: Chat, Stream: true, Responses: true}, ": ping\n\n"},
		{anthropic{}, &Request{Kind: Chat, Stream: true}, "event: ping\ndata: {\"type\":\"ping\"}\n\n"},
	} {
		synctest.Test(t, func(t *testing.T) {
			rec := &syncRecorder{header: http.Header{}}
			out := tc.client.Answer(rec, tc.req, "m")
			time.Sleep(keepaliveEvery - time.Nanosecond)
			synctest.Wait()
			if strings.Contains(rec.String(), tc.ping) {
				t.Fatalf("%T pinged early", tc.client)
			}
			time.Sleep(time.Nanosecond)
			synctest.Wait()
			if strings.Count(rec.String(), tc.ping) != 1 {
				t.Fatalf("%T after silence: %q", tc.client, rec.String())
			}
			out.Close()
		})
	}
}
