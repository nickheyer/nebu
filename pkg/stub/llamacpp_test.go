package stub

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// One slot entry of GET /slots with the task fields llama.cpp adds once the slot ran a task
type wireSlot struct {
	ID                     int     `json:"id"`
	NCtx                   int     `json:"n_ctx"`
	Speculative            *bool   `json:"speculative"`
	IsProcessing           bool    `json:"is_processing"`
	IDTask                 *uint64 `json:"id_task"`
	NPromptTokens          int     `json:"n_prompt_tokens"`
	NPromptTokensProcessed int     `json:"n_prompt_tokens_processed"`
	NPromptTokensCache     int     `json:"n_prompt_tokens_cache"`
	Params                 struct {
		NPredict int `json:"n_predict"`
	} `json:"params"`
	NextToken []struct {
		HasNextToken bool `json:"has_next_token"`
		HasNewLine   bool `json:"has_new_line"`
		NRemain      int  `json:"n_remain"`
		NDecoded     int  `json:"n_decoded"`
	} `json:"next_token"`
}

func slotsOf(t *testing.T, base string) []wireSlot {
	t.Helper()
	r := mustCall(t, "GET", base+"/slots", nil)
	if r.status != http.StatusOK {
		t.Fatalf("slots status %d %s", r.status, r.body)
	}
	return decodeBody[[]wireSlot](t, r)
}

// Slots list every server slot with its context, task fields only once a slot ran a request, and the running one processing
func TestLlamaCppSlots(t *testing.T) {
	s, srv := start(t, Config{Names: NamesLlamaCpp, MaxNumSeqs: 2, BlockSize: 4, NumGPUBlocks: 10, DecodePerToken: 10 * time.Second})
	idle := slotsOf(t, srv.URL)
	if len(idle) != 2 {
		t.Fatalf("%d slots, want 2", len(idle))
	}
	for i, sl := range idle {
		if sl.ID != i || sl.NCtx != 40 || sl.Speculative == nil || *sl.Speculative || sl.IsProcessing || sl.IDTask != nil || sl.NextToken != nil {
			t.Fatalf("idle slot %d %+v", i, sl)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		call(ctx, "POST", srv.URL+"/v1/completions", map[string]any{"prompt": "a b c d e", "max_tokens": 3})
	})
	eventually(t, 5*time.Second, "one running request", func() bool { return s.Stats().Running == 1 })
	busy := slotsOf(t, srv.URL)
	sl := busy[0]
	if !sl.IsProcessing || busy[1].IsProcessing || sl.IDTask == nil || len(sl.NextToken) != 1 || sl.Params.NPredict != 3 {
		t.Fatalf("busy slots %+v", busy)
	}
	nt := sl.NextToken[0]
	if sl.NPromptTokens != 5+nt.NDecoded || sl.NPromptTokensProcessed+sl.NPromptTokensCache != 5 || !nt.HasNextToken || nt.HasNewLine || nt.NRemain != 3-nt.NDecoded {
		t.Fatalf("running slot %+v", sl)
	}
	if r := mustCall(t, "GET", srv.URL+"/slots?fail_on_no_slot=1", nil); r.status != http.StatusOK {
		t.Fatalf("an idle slot remains, fail_on_no_slot answered %d", r.status)
	}
	cancel()
	wg.Wait()
	eventually(t, 5*time.Second, "the request to leave", func() bool { return s.Stats().InFlight == 0 })
	after := slotsOf(t, srv.URL)[0]
	if after.IsProcessing || after.IDTask == nil || *after.IDTask != *sl.IDTask || after.NextToken[0].HasNextToken {
		t.Fatalf("released slot keeps its last task %+v", after)
	}
}

// fail_on_no_slot answers llama.cpp's 503 unavailable error once every slot is busy
func TestLlamaCppSlotsFailOnNoSlot(t *testing.T) {
	s, srv := start(t, Config{Names: NamesLlamaCpp, MaxNumSeqs: 1, DecodePerToken: 10 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	wg.Go(func() {
		call(ctx, "POST", srv.URL+"/v1/completions", map[string]any{"prompt": "a b", "max_tokens": 3})
	})
	eventually(t, 5*time.Second, "the only slot busy", func() bool { return s.Stats().Running == 1 })
	r := mustCall(t, "GET", srv.URL+"/slots?fail_on_no_slot=1", nil)
	e := decodeBody[struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}](t, r)
	if r.status != http.StatusServiceUnavailable || e.Error.Code != 503 || e.Error.Message != "no slot available" || e.Error.Type != "unavailable_error" {
		t.Fatalf("status %d body %s", r.status, r.body)
	}
}

// Slots go to a never used slot first, then to the one released longest ago, as llama.cpp picks them
func TestLlamaCppSlotSeating(t *testing.T) {
	_, srv := start(t, Config{Names: NamesLlamaCpp, MaxNumSeqs: 2})
	for range 3 {
		if r := mustCall(t, "POST", srv.URL+"/v1/completions", map[string]any{"prompt": "a b c", "max_tokens": 1}); r.status != http.StatusOK {
			t.Fatalf("completion %d %s", r.status, r.body)
		}
	}
	slots := slotsOf(t, srv.URL)
	if slots[0].IDTask == nil || slots[1].IDTask == nil || *slots[0].IDTask != 2 || *slots[1].IDTask != 1 {
		t.Fatalf("slot tasks %v %v, want the third request in slot 0 and the second in slot 1", slots[0].IDTask, slots[1].IDTask)
	}
}

// Lora adapters list resident adapters with id, path and scale 1 as llama.cpp does, in llama.cpp mode only
func TestLlamaCppLoraAdapters(t *testing.T) {
	cases := []struct {
		names  Names
		status int
	}{
		{NamesLlamaCpp, http.StatusOK},
		{NamesVLLM, http.StatusNotFound},
		{NamesSGLang, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(string(c.names), func(t *testing.T) {
			_, srv := start(t, Config{Names: c.names, Adapters: []Adapter{{"sql", "/adapters/sql.gguf"}, {"chat", "/adapters/chat.gguf"}}})
			r := mustCall(t, "GET", srv.URL+"/lora-adapters", nil)
			if r.status != c.status {
				t.Fatalf("status %d, want %d", r.status, c.status)
			}
			if c.status != http.StatusOK {
				return
			}
			var list []map[string]json.RawMessage
			if err := json.Unmarshal(r.body, &list); err != nil {
				t.Fatal(err)
			}
			want := []string{`{"id":0,"path":"/adapters/sql.gguf","prompt_prefix":"","scale":1.0,"task_name":""}`, `{"id":1,"path":"/adapters/chat.gguf","prompt_prefix":"","scale":1.0,"task_name":""}`}
			if len(list) != 2 {
				t.Fatalf("adapters %s", r.body)
			}
			for i, entry := range list {
				got, _ := json.Marshal(entry)
				if string(got) != want[i] {
					t.Fatalf("adapter %d %s, want %s", i, got, want[i])
				}
			}
		})
	}
}

// Slots answer only in llama.cpp mode
func TestSlotsRouteByMode(t *testing.T) {
	for _, names := range []Names{NamesVLLM, NamesSGLang} {
		_, srv := start(t, Config{Names: names})
		if r := mustCall(t, "GET", srv.URL+"/slots", nil); r.status != http.StatusNotFound || !strings.Contains(string(r.body), "404") {
			t.Fatalf("%s slots answered %d %s", names, r.status, r.body)
		}
	}
}
