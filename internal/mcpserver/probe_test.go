package mcpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A stored model and a fit table answer without tensor tables, digests and untouched params, and whole when asked
func TestLeanAnswers(t *testing.T) {
	cs := poolSession(t, node(t))
	fit := call(t, cs, "inspect_model", map[string]any{"source_id": "disk", "repo": "org/model", "groups": []string{"Q4_K_M"}, "runtime_ids": []string{"llamacpp"}, "contexts": []int{2048}, "params": map[string]string{"n_ubatch": "64"}}, "")
	plan := fit["rows"].([]any)[0].(map[string]any)["plan"].(map[string]any)
	if params := plan["params"].(map[string]any); params["n_ubatch"] != "64" || params["n_ctx"] != "2048" || params["alias"] != nil || params["n_batch"] != nil || len(params) > 12 {
		t.Fatalf("a plan answers the params it moved off the defaults: %v", params)
	}
	if d := fit["descriptors"].([]any)[0].(map[string]any); d["groups"] != nil || d["architecture"] != "llama" {
		t.Fatalf("descriptor %v", d)
	}
	if file := fit["model"].(map[string]any)["artifacts"].([]any)[0].(map[string]any); file["url"] != nil || file["sha256"] != nil || file["path"] == nil {
		t.Fatalf("artifact %v", file)
	}
	whole := call(t, cs, "inspect_model", map[string]any{"source_id": "disk", "repo": "org/model", "groups": []string{"Q4_K_M"}, "runtime_ids": []string{"llamacpp"}, "contexts": []int{2048}, "full": true}, "")
	if d := whole["descriptors"].([]any)[0].(map[string]any); len(d["groups"].([]any)) == 0 {
		t.Fatalf("a whole descriptor %v", d)
	}
	if params := whole["rows"].([]any)[0].(map[string]any)["plan"].(map[string]any)["params"].(map[string]any); len(params) < 50 {
		t.Fatalf("a whole plan %v", params)
	}
	stored := call(t, cs, "list_models", map[string]any{}, "")["models"].([]any)[0].(map[string]any)
	if file := stored["artifacts"].([]any)[0].(map[string]any); stored["repo"] != "org/stored" || file["digest"] != nil || !strings.HasSuffix(file["path"].(string), "weights.fake") {
		t.Fatalf("stored model %v", stored)
	}
	if one := call(t, cs, "get_model", map[string]any{"source_id": "src", "repo": "org/stored", "group": "q4", "full": true}, "")["model"].(map[string]any); one["artifacts"].([]any)[0].(map[string]any)["digest"] != "sha256:m" {
		t.Fatalf("a whole stored model %v", one)
	}
	if host := call(t, cs, "host_profile", map[string]any{}, "")["profile"].(map[string]any); len(host["devices"].([]any)) != 1 || host["probes"] != nil {
		t.Fatalf("host %v", host)
	}
}

// A chat goes to the route as a stream carrying what the call added to it, and answers the reply with the tokens and speeds measured
func TestChatMeasures(t *testing.T) {
	var sent map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"ext"}]}`)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &sent)
		w.Header().Set("Content-Type", "text/event-stream")
		flush := w.(http.Flusher)
		time.Sleep(60 * time.Millisecond)
		for _, chunk := range []string{
			`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"Rea"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"dy."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":9,"total_tokens":129}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flush.Flush()
			time.Sleep(20 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)
	addr := strings.TrimPrefix(upstream.URL, "http://")
	_, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	cs := poolSession(t)
	call(t, cs, "put_pool", map[string]any{"id": "static", "model": "ext", "target_ports": []int{n}, "failure_mode": "fail-open", "static_endpoints": []map[string]any{{"address": addr}}}, "")
	call(t, cs, "put_route", map[string]any{"name": "ext", "rules": []map[string]any{{"targets": []map[string]any{{"pool_id": "static", "weight": 1}}}}}, "")
	got := call(t, cs, "chat", map[string]any{"model": "ext", "prompt": "Say ready.", "max_tokens": 32, "pad_tokens": 100, "body": map[string]any{"temperature": 0.5}}, "")
	if got["reply"] != "Ready." || got["reasoning"] != "thinking" || got["finish_reason"] != "stop" || got["prompt_tokens"] != float64(120) || got["completion_tokens"] != float64(9) {
		t.Fatalf("answer %v", got)
	}
	first, total := got["first_token_seconds"].(float64), got["total_seconds"].(float64)
	if first < 0.05 || total <= first || got["prompt_tokens_per_second"].(float64) <= 0 || got["completion_tokens_per_second"].(float64) <= 0 {
		t.Fatalf("timings %v", got)
	}
	prompt := sent["messages"].([]any)[0].(map[string]any)["content"].(string)
	if sent["stream"] != true || sent["temperature"] != 0.5 || !strings.HasPrefix(prompt, "Filler ") || !strings.HasSuffix(prompt, "Say ready.") || len(strings.Fields(prompt)) < 100 {
		t.Fatalf("sent %v", sent)
	}
	call(t, cs, "chat", map[string]any{"model": "ext"}, "a prompt or messages")
	// The whole messages array goes as given, with no filler put before it
	call(t, cs, "chat", map[string]any{"model": "ext", "messages": []map[string]any{{"role": "system", "content": "Be brief."}, {"role": "user", "content": "Say ready."}}}, "")
	if turns := sent["messages"].([]any); len(turns) != 2 || turns[0].(map[string]any)["content"] != "Be brief." {
		t.Fatalf("messages sent %v", sent["messages"])
	}
}
