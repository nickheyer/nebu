package runtimes

import (
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A launch that generates nothing reports embedding or rerank alone, every other launch its input modalities
func TestPoolingLaunchModalities(t *testing.T) {
	profile, plan := plannedHost()
	desc := &v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_LANGUAGE, Params: map[string]float64{"n_ctx_train": 8192}, Precision: &v1.Precision{Label: "Q8_0"},
		Groups: []*v1.TensorGroup{{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER}, {Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION}}}
	in := Launch{Descriptor: desc, Plan: plan, Profile: profile}
	cases := []struct {
		name   string
		rt     Runtime
		params map[string]string
		want   string
	}{
		{"llama.cpp chat", LlamaCpp{}, nil, "text"},
		{"llama.cpp embedding", LlamaCpp{}, map[string]string{"embedding": "true"}, ModalityEmbedding},
		{"llama.cpp embedding mean pooling", LlamaCpp{}, map[string]string{"embedding": "true", "pooling": "mean"}, ModalityEmbedding},
		{"llama.cpp embedding rank pooling", LlamaCpp{}, map[string]string{"embedding": "true", "pooling": "rank"}, ModalityRerank},
		{"llama.cpp reranking", LlamaCpp{}, map[string]string{"reranking": "true"}, ModalityRerank},
		{"sglang chat", SGLang{}, nil, "text,image"},
		{"sglang embedding", SGLang{}, map[string]string{"is_embedding": "true"}, ModalityEmbedding},
		{"sglang embedding off", SGLang{}, map[string]string{"is_embedding": "false"}, "text,image"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inv, err := c.rt.Inventory(in, c.params)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(inv.GetModalities(), ","); got != c.want {
				t.Fatalf("modalities %s, want %s", got, c.want)
			}
			if inv.GetContextLength() == 0 || inv.GetQuantization() != "Q8_0" || inv.GetAccelerator() != "2x NVIDIA H100 80GB HBM3" || inv.GetThroughputClass() != ThroughputDevice {
				t.Fatalf("the rest of the inventory %v", inv)
			}
		})
	}
}

// An endpoint spanning launches keeps the serving launch's context and counts every launch's devices at the slowest class
func TestSpanInventory(t *testing.T) {
	head := &v1.RuntimeInventory{ContextLength: 32768, Quantization: "Q4_K_M", Accelerator: "2x NVIDIA H100 80GB HBM3", Modalities: []string{"text", "image"}, ThroughputClass: ThroughputDevice}
	inv := func(acc, class string) *v1.RuntimeInventory {
		return &v1.RuntimeInventory{ContextLength: 4096, Quantization: "other", Accelerator: acc, Modalities: []string{"text"}, ThroughputClass: class}
	}
	cases := []struct {
		name     string
		launches []*v1.RuntimeInventory
		acc      string
		class    string
	}{
		{"head alone", []*v1.RuntimeInventory{head}, "2x NVIDIA H100 80GB HBM3", ThroughputDevice},
		{"same devices summed", []*v1.RuntimeInventory{head, inv("1x NVIDIA H100 80GB HBM3", ThroughputDevice)}, "3x NVIDIA H100 80GB HBM3", ThroughputDevice},
		{"mixed devices in first seen order", []*v1.RuntimeInventory{head, inv("1x AMD MI300X + 1x NVIDIA H100 80GB HBM3", ThroughputDevice)}, "3x NVIDIA H100 80GB HBM3 + 1x AMD MI300X", ThroughputDevice},
		{"a host stage makes an offload run", []*v1.RuntimeInventory{head, inv("cpu", ThroughputHost)}, "2x NVIDIA H100 80GB HBM3 + cpu", ThroughputOffload},
		{"an offloading stage makes an offload run", []*v1.RuntimeInventory{head, inv("1x NVIDIA L4", ThroughputOffload)}, "2x NVIDIA H100 80GB HBM3 + 1x NVIDIA L4", ThroughputOffload},
		{"every launch on host", []*v1.RuntimeInventory{inv("cpu", ThroughputHost), inv("cpu", ThroughputHost)}, "cpu", ThroughputHost},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := SpanInventory(head, c.launches)
			if err != nil {
				t.Fatal(err)
			}
			if got.GetAccelerator() != c.acc || got.GetThroughputClass() != c.class {
				t.Fatalf("accelerator %q class %q, want %q %q", got.GetAccelerator(), got.GetThroughputClass(), c.acc, c.class)
			}
			if got.GetContextLength() != 32768 || got.GetQuantization() != "Q4_K_M" || strings.Join(got.GetModalities(), ",") != "text,image" {
				t.Fatalf("the serving launch's fields %v", got)
			}
		})
	}
	if head.GetAccelerator() != "2x NVIDIA H100 80GB HBM3" {
		t.Fatalf("the serving inventory was changed in place %v", head)
	}
	for _, bad := range [][]*v1.RuntimeInventory{nil, {inv("two H100", ThroughputDevice)}, {inv("0x NVIDIA H100", ThroughputDevice)}, {inv("1x NVIDIA H100", "")}} {
		if _, err := SpanInventory(head, bad); err == nil {
			t.Errorf("launches %v span without a refusal", bad)
		}
	}
	if _, err := SpanInventory(nil, []*v1.RuntimeInventory{head}); err == nil {
		t.Error("a serving launch without an inventory")
	}
}
