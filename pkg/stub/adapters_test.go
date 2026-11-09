package stub

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"
)

// Ids served on /v1/models in order
func modelIDs(t *testing.T, base string) []string {
	t.Helper()
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(mustCall(t, "GET", base+"/v1/models", nil).body, &list); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range list.Data {
		ids = append(ids, d.ID)
	}
	return ids
}

// Sends a chat request in the background and delivers its reply
func chatAsync(base, model string) <-chan reply {
	out := make(chan reply, 1)
	go func() {
		r, _ := call(context.Background(), "POST", base+"/v1/chat/completions", chatBody(model, "hi", map[string]any{"max_tokens": 1}))
		out <- r
	}()
	return out
}

func TestAdapterLoadAndWait(t *testing.T) {
	s, srv := start(t, Config{Model: "base", MaxLoras: 1, LoraLoadDelay: 100 * time.Millisecond, Mode: ModeRatio, Speedup: 2})
	load := func(name, path string) reply {
		return mustCall(t, "POST", srv.URL+"/v1/load_lora_adapter", map[string]any{"lora_name": name, "lora_path": path})
	}
	unload := func(name string) reply {
		return mustCall(t, "POST", srv.URL+"/v1/unload_lora_adapter", map[string]any{"lora_name": name})
	}
	begin := time.Now()
	if r := load("a", "/a"); r.status != http.StatusOK || string(r.body) != "Success: LoRA adapter 'a' added successfully." {
		t.Fatalf("load a %d %q", r.status, r.body)
	}
	if took := time.Since(begin); took < 50*time.Millisecond {
		t.Fatalf("load returned after %v, before the 50ms wall delay", took)
	}
	wantError(t, load("a", "/a"), http.StatusBadRequest, "")
	wantError(t, load("base", "/x"), http.StatusBadRequest, "")
	wantError(t, load("x", ""), http.StatusBadRequest, "")
	if r := load("b", "/b"); r.status != http.StatusAccepted {
		t.Fatalf("load b at capacity %d %q", r.status, r.body)
	}
	if ids := modelIDs(t, srv.URL); !slices.Equal(ids, []string{"base", "a"}) {
		t.Fatalf("models %v", ids)
	}
	l := scrape(t, srv.URL)["vllm:lora_requests_info"]
	if l.labels["running_lora_adapters"] != "a" || l.labels["waiting_lora_adapters"] != "b" || l.labels["max_lora"] != "1" {
		t.Fatalf("lora labels %v", l.labels)
	}
	waitingB := chatAsync(srv.URL, "b")
	eventually(t, 5*time.Second, "request for b to queue", func() bool { return s.Stats().Queued == 1 })
	if r := <-chatAsync(srv.URL, "a"); r.status != http.StatusOK {
		t.Fatalf("request for resident a behind a waiting adapter %d %s", r.status, r.body)
	}
	if r := unload("a"); r.status != http.StatusOK || string(r.body) != "Success: LoRA adapter 'a' removed successfully." {
		t.Fatalf("unload a %d %q", r.status, r.body)
	}
	r := <-waitingB
	if r.status != http.StatusOK || decodeBody[wireCompletion](t, r).Model != "b" {
		t.Fatalf("request for b after its load %d %s", r.status, r.body)
	}
	if ids := modelIDs(t, srv.URL); !slices.Equal(ids, []string{"base", "b"}) {
		t.Fatalf("models %v", ids)
	}
	l = scrape(t, srv.URL)["vllm:lora_requests_info"]
	if l.labels["running_lora_adapters"] != "b" || l.labels["waiting_lora_adapters"] != "" {
		t.Fatalf("lora labels %v", l.labels)
	}
	wantError(t, unload("zzz"), http.StatusNotFound, "lora_not_found")
	wantError(t, mustCall(t, "POST", srv.URL+"/v1/chat/completions", chatBody("zzz", "hi", nil)), http.StatusNotFound, "model_not_found")

	if r := load("c", "/c"); r.status != http.StatusAccepted {
		t.Fatalf("load c %d", r.status)
	}
	waitingC := chatAsync(srv.URL, "c")
	eventually(t, 5*time.Second, "request for c to queue", func() bool { return s.Stats().Queued == 1 })
	unload("c")
	wantError(t, <-waitingC, http.StatusNotFound, "model_not_found")

	unload("b")
	loading := make(chan reply, 1)
	go func() { loading <- load("e", "/e") }()
	eventually(t, 5*time.Second, "e to start loading", func() bool { return s.eng.gauges().pending == "e" })
	unload("e")
	wantError(t, <-loading, http.StatusConflict, "lora_unloaded")
	if st := s.Stats(); st.InFlight != 0 {
		t.Fatalf("in flight %d at the end", st.InFlight)
	}
}
