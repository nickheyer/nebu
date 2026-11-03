package stub

import (
	"net/http"
	"testing"
	"time"
)

// SGLang mode serves server info and its alias with the batch cap at top level, vLLM mode serves neither
func TestSGLangServerInfo(t *testing.T) {
	cases := []struct {
		name   string
		names  Names
		path   string
		status int
	}{
		{"sglang route", NamesSGLang, "/server_info", http.StatusOK},
		{"sglang deprecated alias", NamesSGLang, "/get_server_info", http.StatusOK},
		{"vllm has no server info", NamesVLLM, "/server_info", http.StatusNotFound},
		{"vllm has no alias", NamesVLLM, "/get_server_info", http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, srv := start(t, Config{Names: c.names, MaxLoras: 3, MaxNumSeqs: 8, BlockSize: 16, NumGPUBlocks: 100})
			r := mustCall(t, "GET", srv.URL+c.path, nil)
			if r.status != c.status {
				t.Fatalf("status %d, want %d: %s", r.status, c.status, r.body)
			}
			if c.status != http.StatusOK {
				return
			}
			info := decodeBody[struct {
				ModelPath         string           `json:"model_path"`
				MaxLoRAs          int              `json:"max_loras_per_batch"`
				PageSize          int              `json:"page_size"`
				MaxRunning        int              `json:"max_running_requests"`
				EnableLoRA        bool             `json:"enable_lora"`
				Status            string           `json:"status"`
				MaxTotalNumTokens int              `json:"max_total_num_tokens"`
				InternalStates    []map[string]any `json:"internal_states"`
				Frontend          string           `json:"frontend"`
			}](t, r)
			if info.ModelPath != "stub-model" || info.MaxLoRAs != 3 || info.PageSize != 16 || info.MaxRunning != 8 || !info.EnableLoRA || info.Status != "ready" ||
				info.MaxTotalNumTokens != 1600 || len(info.InternalStates) != 1 || info.Frontend != "python" {
				t.Fatalf("server info %s", r.body)
			}
		})
	}
}

// SGLang mode lists registered adapters as model cards owned by sglang whose parent is the served model
func TestSGLangModelListing(t *testing.T) {
	_, srv := start(t, Config{Names: NamesSGLang, Model: "base", Adapters: []Adapter{{"sql", "/adapters/sql"}, {"chat", "/adapters/chat"}}})
	list := decodeBody[struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string  `json:"id"`
			OwnedBy string  `json:"owned_by"`
			Root    string  `json:"root"`
			Parent  *string `json:"parent"`
		} `json:"data"`
	}](t, mustCall(t, "GET", srv.URL+"/v1/models", nil))
	if list.Object != "list" || len(list.Data) != 3 || list.Data[0].ID != "base" || list.Data[0].Parent != nil || list.Data[0].OwnedBy != "sglang" {
		t.Fatalf("listing %+v", list)
	}
	for i, want := range []string{"sql", "chat"} {
		a := list.Data[i+1]
		if a.ID != want || a.Parent == nil || *a.Parent != "base" || a.Root != "/adapters/"+want || a.OwnedBy != "sglang" {
			t.Fatalf("adapter card %d %+v", i, a)
		}
	}
}

// Server info answers 503 while the model loads, as the other model endpoints do
func TestSGLangServerInfoWhileLoading(t *testing.T) {
	_, srv := start(t, Config{Names: NamesSGLang, LoadTime: time.Minute})
	wantError(t, mustCall(t, "GET", srv.URL+"/server_info", nil), http.StatusServiceUnavailable, "model_loading")
}
