package stub

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// lengths encodes words as their letter counts and decodes ids back to runs of x
type lengths struct{}

func (lengths) Encode(text string, special bool) ([]uint32, error) {
	var ids []uint32
	if special {
		ids = append(ids, 1)
	}
	for _, w := range strings.Fields(text) {
		ids = append(ids, uint32(len(w)))
	}
	return ids, nil
}

func (lengths) Decode(ids []uint32) (string, error) {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strings.Repeat("x", int(id))
	}
	return strings.Join(parts, " "), nil
}

func post(t *testing.T, srv *httptest.Server, path, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// return_token_ids puts the prompt ids and each piece's ids on the answer, spelled by the configured tokenizer
func TestReturnTokenIDs(t *testing.T) {
	s, err := New(Config{Tokenizer: lengths{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := httptest.NewServer(s)
	defer srv.Close()
	resp := post(t, srv, "/v1/completions", `{"model":"stub-model","prompt":"ab cde","max_tokens":2,"return_token_ids":true}`)
	defer resp.Body.Close()
	var body struct {
		Choices []struct {
			Text           string   `json:"text"`
			PromptTokenIDs []uint32 `json:"prompt_token_ids"`
			TokenIDs       []uint32 `json:"token_ids"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(body.Choices) != 1 {
		t.Fatalf("status %d body %+v", resp.StatusCode, body)
	}
	c := body.Choices[0]
	if len(c.PromptTokenIDs) != 3 || c.PromptTokenIDs[0] != 1 || c.PromptTokenIDs[1] != 2 || c.PromptTokenIDs[2] != 3 {
		t.Fatalf("prompt ids %v", c.PromptTokenIDs)
	}
	want, _ := lengths{}.Encode(c.Text, false)
	if len(c.TokenIDs) != len(want) {
		t.Fatalf("token ids %v for %q", c.TokenIDs, c.Text)
	}
	resp = post(t, srv, "/v1/completions", `{"model":"stub-model","prompt":[1,2,3],"echo":true,"max_tokens":1}`)
	defer resp.Body.Close()
	var echoed struct {
		Choices []struct {
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&echoed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(echoed.Choices[0].Text, "x xx xxx") {
		t.Fatalf("echo %q", echoed.Choices[0].Text)
	}
}

// A streamed chat carries the prompt ids on its first chunk and token ids on every piece
func TestReturnTokenIDsStream(t *testing.T) {
	s, err := New(Config{Tokenizer: lengths{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := httptest.NewServer(s)
	defer srv.Close()
	resp := post(t, srv, "/v1/chat/completions", `{"model":"stub-model","messages":[{"role":"user","content":"hi"}],"max_tokens":2,"stream":true,"return_token_ids":true}`)
	defer resp.Body.Close()
	var chunks []map[string]any
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, m)
	}
	if len(chunks) < 3 {
		t.Fatalf("chunks %d", len(chunks))
	}
	if _, ok := chunks[0]["prompt_token_ids"]; !ok {
		t.Fatalf("first chunk lacks prompt ids: %v", chunks[0])
	}
	if _, ok := chunks[1]["prompt_token_ids"]; ok {
		t.Fatalf("second chunk repeats prompt ids: %v", chunks[1])
	}
	choice := chunks[1]["choices"].([]any)[0].(map[string]any)
	if _, ok := choice["token_ids"]; !ok {
		t.Fatalf("piece lacks token ids: %v", chunks[1])
	}
}
