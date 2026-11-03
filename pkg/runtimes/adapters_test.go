package runtimes

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// One request a fake server saw
type seen struct {
	method, path, body string
}

// A server recording every request and answering each path from the table, 404 for any other
func recorder(t *testing.T, answers map[string]string) (*httptest.Server, func() []seen) {
	t.Helper()
	var mu sync.Mutex
	var log []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		log = append(log, seen{r.Method, r.URL.RequestURI(), string(body)})
		mu.Unlock()
		answer, ok := answers[r.Method+" "+r.URL.RequestURI()]
		if !ok {
			http.Error(w, "no route", http.StatusNotFound)
			return
		}
		w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []seen {
		mu.Lock()
		defer mu.Unlock()
		return append([]seen(nil), log...)
	}
}

const modelsListing = `{"object":"list","data":[
	{"id":"base","object":"model","root":"/models/base","parent":null},
	{"id":"sql","object":"model","root":"/adapters/sql","parent":"base"}
]}`

// vLLM and SGLang load and unload by name with the exact bodies their servers take, and list adapters by parent
func TestNamedAdapters(t *testing.T) {
	for _, c := range []struct {
		rt           Runtime
		load, unload string
	}{
		{VLLM{}, "/v1/load_lora_adapter", "/v1/unload_lora_adapter"},
		{SGLang{}, "/load_lora_adapter", "/unload_lora_adapter"},
	} {
		srv, requests := recorder(t, map[string]string{
			"GET /v1/models":   modelsListing,
			"POST " + c.load:   "Success: LoRA adapter 'sql' added successfully",
			"POST " + c.unload: "Success: LoRA adapter 'sql' removed successfully",
		})
		client := c.rt.Adapters(srv.URL, map[string]string{"enable_lora": "true"})
		ctx := context.Background()
		list, err := client.List(ctx)
		if err != nil || len(list) != 1 || list[0] != (Adapter{Name: "sql", Path: "/adapters/sql"}) {
			t.Fatalf("%s list %v %v", c.rt.ID(), list, err)
		}
		if err := client.Load(ctx, "sql", "/adapters/sql"); err != nil {
			t.Fatalf("%s load %v", c.rt.ID(), err)
		}
		if err := client.Unload(ctx, "sql"); err != nil {
			t.Fatalf("%s unload %v", c.rt.ID(), err)
		}
		got := requests()
		want := []seen{
			{"GET", "/v1/models", ""},
			{"POST", c.load, `{"lora_name":"sql","lora_path":"/adapters/sql"}`},
			{"POST", c.unload, `{"lora_name":"sql"}`},
		}
		if len(got) != len(want) {
			t.Fatalf("%s requests %v", c.rt.ID(), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s request %d %+v, want %+v", c.rt.ID(), i, got[i], want[i])
			}
		}
	}
	// A refusal carries the server's own words
	srv, _ := recorder(t, map[string]string{})
	if err := (VLLM{}).Adapters(srv.URL, map[string]string{"enable_lora": "true"}).Load(context.Background(), "x", "/x"); err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "no route") {
		t.Fatalf("refusal %v", err)
	}
}

// llama.cpp sets one adapter's scale and posts every other adapter's current scale with it
func TestLlamaAdapters(t *testing.T) {
	listing := `[{"id":0,"path":"/m/sql.gguf","scale":0.0},{"id":1,"path":"/m/chat.gguf","scale":0.5}]`
	srv, requests := recorder(t, map[string]string{"GET /lora-adapters": listing, "POST /lora-adapters": `{"success":true}`})
	client := (LlamaCpp{}).Adapters(srv.URL, map[string]string{"lora": "/m/sql.gguf,/m/chat.gguf"})
	ctx := context.Background()
	list, err := client.List(ctx)
	if err != nil || len(list) != 1 || list[0] != (Adapter{Name: "chat", Path: "/m/chat.gguf"}) {
		t.Fatalf("applied adapters %v %v", list, err)
	}
	if err := client.Load(ctx, "sql", "/m/sql.gguf"); err != nil {
		t.Fatal(err)
	}
	if err := client.Load(ctx, "sql", ""); err != nil {
		t.Fatal(err)
	}
	if err := client.Unload(ctx, "chat"); err != nil {
		t.Fatal(err)
	}
	var posts []string
	for _, r := range requests() {
		if r.method == http.MethodPost {
			posts = append(posts, r.body)
		}
	}
	want := []string{
		`[{"id":0,"scale":1},{"id":1,"scale":0.5}]`,
		`[{"id":0,"scale":1},{"id":1,"scale":0.5}]`,
		`[{"id":0,"scale":0},{"id":1,"scale":0}]`,
	}
	if strings.Join(posts, "\n") != strings.Join(want, "\n") {
		t.Fatalf("posted\n%s\nwant\n%s", strings.Join(posts, "\n"), strings.Join(want, "\n"))
	}
	if err := client.Load(ctx, "other", "/elsewhere/other.gguf"); err == nil || !strings.Contains(err.Error(), "loaded at launch") {
		t.Fatalf("an adapter llama-server never loaded: %v", err)
	}
}

// vLLM sleeps at level one and wakes through its dev mode endpoints
func TestVLLMSleeper(t *testing.T) {
	srv, requests := recorder(t, map[string]string{"POST /sleep?level=1": "", "POST /wake_up": ""})
	s := (VLLM{}).Sleeper(srv.URL, map[string]string{"enable_sleep_mode": "true"})
	ctx := context.Background()
	if err := s.Sleep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Wake(ctx); err != nil {
		t.Fatal(err)
	}
	got := requests()
	if len(got) != 2 || got[0] != (seen{"POST", "/sleep?level=1", ""}) || got[1] != (seen{"POST", "/wake_up", ""}) {
		t.Fatalf("requests %v", got)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Sleep(cancelled); err == nil {
		t.Fatal("a cancelled context ends the call")
	}
}
