package stub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Starts a stub behind an httptest server, both closed with the test
func start(t *testing.T, cfg Config) (*Stub, *httptest.Server) {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s)
	t.Cleanup(func() {
		s.Close()
		srv.Close()
	})
	return s, srv
}

// A request body sent as is
type rawBody string

type reply struct {
	status int
	header http.Header
	body   []byte
}

// Sends a JSON body with header pairs and reads the whole reply
func call(ctx context.Context, method, url string, body any, header ...string) (reply, error) {
	var rd io.Reader
	if raw, ok := body.(rawBody); ok {
		rd = strings.NewReader(string(raw))
	} else if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return reply{}, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, header: resp.Header, body: b}, err
}

// Sends a request and fails the test on transport errors
func mustCall(t *testing.T, method, url string, body any, header ...string) reply {
	t.Helper()
	r, err := call(context.Background(), method, url, body, header...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Unmarshals a reply and fails the test on malformed JSON
func decodeBody[T any](t *testing.T, r reply) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
	return v
}

// Data payloads of a complete server sent event stream
func sseData(t *testing.T, body []byte) []string {
	t.Helper()
	var out []string
	for _, ev := range strings.Split(strings.TrimSpace(string(body)), "\n\n") {
		data, ok := strings.CutPrefix(ev, "data: ")
		if !ok {
			t.Fatalf("event without data %q", ev)
		}
		out = append(out, data)
	}
	return out
}

// Polls until the condition holds and fails the test once the timeout passes
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Text the engine writes for n tokens
func words(n int, chat bool) string {
	var b strings.Builder
	for i := range n {
		if i > 0 || !chat {
			b.WriteByte(' ')
		}
		b.WriteString(vocab[i%len(vocab)])
	}
	return b.String()
}

// A prompt of n distinct single token words
func promptOf(prefix string, n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = prefix + strconv.Itoa(i)
	}
	return strings.Join(parts, " ")
}

// Chat request with one user message
func chatBody(model, content string, extra map[string]any) map[string]any {
	b := map[string]any{"model": model, "messages": []map[string]any{{"role": "user", "content": content}}}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

type wireUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type wireCompletion struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int    `json:"index"`
		Text    string `json:"text"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage wireUsage `json:"usage"`
}

type wireError struct {
	Error struct {
		Message string  `json:"message"`
		Type    string  `json:"type"`
		Param   *string `json:"param"`
		Code    *string `json:"code"`
	} `json:"error"`
}

// Asserts an OpenAI error reply with a status and optionally a code
func wantError(t *testing.T, r reply, status int, code string) wireError {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d, want %d, body %s", r.status, status, r.body)
	}
	e := decodeBody[wireError](t, r)
	if e.Error.Message == "" || e.Error.Type == "" {
		t.Fatalf("error body missing message or type %s", r.body)
	}
	if code != "" && (e.Error.Code == nil || *e.Error.Code != code) {
		t.Fatalf("error code %v, want %s", e.Error.Code, code)
	}
	return e
}

// Pointer to a value
func ptr[T any](v T) *T { return &v }
