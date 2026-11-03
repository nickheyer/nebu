package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

// Publishes a ready fake runtime instance whose launch reported its inventory, naming a route after it
func publishLaunch(t *testing.T, h *harness, name string, params map[string]string, port int) {
	t.Helper()
	launch := runtimes.Launch{Descriptor: &v1.Descriptor{Precision: &v1.Precision{Label: "Q4_K_M"}}}
	inv, err := fake.Runtime{}.Inventory(launch, params)
	if err != nil {
		t.Fatal(err)
	}
	rec := &v1.Instance{Id: name, Name: name, SourceId: "hf", Repo: "org/" + name, Group: "q4", RuntimeId: fake.RuntimeID, Params: params, Inventory: inv}
	mem, err := instances.MembershipOf(fake.Runtime{}, rec, "", inv.GetAccelerator())
	if err != nil {
		t.Fatal(err)
	}
	owner := pools.OwnerRef{Kind: pools.OwnerInstance, ID: name}
	ep := pools.Published{InstanceID: name, NodeID: testNode, Address: datalayer.Address{Host: "127.0.0.1", Port: port}, Ready: true}
	if err := h.mgr.Publish(context.Background(), owner, mem, []pools.Published{ep}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.NameRoute(context.Background(), owner, name, pools.RouteSpec{}); err != nil {
		t.Fatal(err)
	}
}

// The models listings show a launch's context, quantization, accelerator and class, and a pooling launch's one output alone
func TestModelsListInventory(t *testing.T) {
	h := newHarness(t)
	publishLaunch(t, h, "chat", map[string]string{"n_ctx": "8192"}, 18001)
	publishLaunch(t, h, "embed", map[string]string{"n_ctx": "512", fake.ParamConvert: "embed"}, 18002)
	publishLaunch(t, h, "rank", map[string]string{"n_ctx": "512", runtimes.VLLMSupportedTasks: "classify,token_classify"}, 18003)
	waitFor(t, "every route listed", func() bool {
		_, a := h.g.pools.Route("chat")
		_, b := h.g.pools.Route("embed")
		_, c := h.g.pools.Route("rank")
		return a && b && c
	})
	var openai struct {
		Data []struct {
			ID           string   `json:"id"`
			Capabilities []string `json:"capabilities"`
			Inventory    struct {
				ContextLength   uint32   `json:"context_length"`
				Quantization    string   `json:"quantization"`
				Accelerator     string   `json:"accelerator"`
				Modalities      []string `json:"modalities"`
				ThroughputClass string   `json:"throughput_class"`
			} `json:"inventory"`
		} `json:"data"`
	}
	r := h.do(http.MethodGet, "/v1/models", "")
	if r.status != http.StatusOK {
		t.Fatalf("models %d %s", r.status, r.body)
	}
	if err := json.Unmarshal(r.body, &openai); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		context    uint32
		caps       []string
		modalities []string
	}{
		"chat":  {8192, []string{"completion", "tools"}, []string{"text"}},
		"embed": {512, []string{runtimes.ModalityEmbedding}, []string{runtimes.ModalityEmbedding}},
		"rank":  {512, []string{runtimes.ModalityClassify}, []string{runtimes.ModalityClassify}},
	}
	if len(openai.Data) != len(want) {
		t.Fatalf("listed %s", r.body)
	}
	for _, m := range openai.Data {
		w, ok := want[m.ID]
		inv := m.Inventory
		if !ok || inv.ContextLength != w.context || inv.Quantization != "Q4_K_M" || inv.Accelerator != "cpu" || inv.ThroughputClass != runtimes.ThroughputHost || !slices.Equal(inv.Modalities, w.modalities) || !slices.Equal(m.Capabilities, w.caps) {
			t.Fatalf("model %s listed as %+v", m.ID, m)
		}
	}
	var tags struct {
		Models []struct {
			Name    string `json:"name"`
			Details struct {
				Quantization string `json:"quantization_level"`
			} `json:"details"`
		} `json:"models"`
	}
	r = h.do(http.MethodGet, "/api/tags", "")
	if err := json.Unmarshal(r.body, &tags); err != nil || len(tags.Models) != 3 || tags.Models[0].Details.Quantization != "Q4_K_M" || tags.Models[2].Details.Quantization != "Q4_K_M" {
		t.Fatalf("ollama tags %d %s", r.status, r.body)
	}
	var show struct {
		Capabilities []string       `json:"capabilities"`
		Info         map[string]any `json:"model_info"`
	}
	r = h.do(http.MethodPost, "/api/show", `{"model":"embed"}`)
	if err := json.Unmarshal(r.body, &show); err != nil || !slices.Equal(show.Capabilities, []string{runtimes.ModalityEmbedding}) || show.Info["nebu.context_length"] != float64(512) || show.Info["nebu.accelerator"] != "cpu" || show.Info["nebu.throughput_class"] != runtimes.ThroughputHost {
		t.Fatalf("ollama show %d %s", r.status, r.body)
	}
}
