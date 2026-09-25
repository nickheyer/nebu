package formations

import (
	"math"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A solo request teaches bytes a token read, the device weights at the share a token touches plus
// the cache at the request's context, and the parameters a token touched, experts at their share
func TestSoloSample(t *testing.T) {
	d := &v1.Descriptor{Params: map[string]float64{"n_expert": 8, "n_expert_used": 2}}
	d.Groups = append(d.Groups,
		&v1.TensorGroup{Id: "embedding", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, Layer: -1, Bytes: 1 << 30, Elements: 500e6},
		&v1.TensorGroup{Id: "layer.0", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Layer: 0, Bytes: 40 << 30, Elements: 20e9},
		&v1.TensorGroup{Id: "experts.0", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, Layer: 0, Bytes: 16 << 30, Elements: 8e9},
	)
	plan := &v1.MemoryPlan{
		CacheBytes: 2 << 30,
		Params:     map[string]string{"n_ctx": "8192"},
		Placements: []*v1.GroupPlacement{
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, PoolId: "device", Bytes: 40 << 30},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, PoolId: "device", Bytes: 16 << 30},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, PoolId: "host", Bytes: 1 << 30},
		},
	}
	trace := &v1.Trace{PromptTokens: 1000, CompletionTokens: 200}
	s, err := soloSample("gpu0", plan, "n_ctx", d, trace, 1.5, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	perToken := float64(2<<30) / 8192
	wantBytes := float64(40<<30) + float64(16<<30)*0.25 + perToken*1200
	if math.Abs(s.BytesPerToken-wantBytes) > 1 {
		t.Fatalf("bytes per token %.0f, want %.0f", s.BytesPerToken, wantBytes)
	}
	if want := 500e6 + 20e9 + 8e9*0.25; s.Parameters != want {
		t.Fatalf("parameters %.0f, want %.0f", s.Parameters, want)
	}
	if s.DeviceID != "gpu0" || s.PromptTokens != 1000 || s.TimeToFirst != 1.5 || s.TimePerToken != 0.05 || !s.Decode || !s.Prefill {
		t.Fatalf("sample %+v", s)
	}
	// Short requests teach nothing on the side they are short on.
	short, err := soloSample("gpu0", plan, "n_ctx", d, &v1.Trace{PromptTokens: 100, CompletionTokens: 8}, 0.2, 0.05)
	if err != nil || short.Decode || short.Prefill {
		t.Fatalf("short request %+v %v", short, err)
	}
	// A cache with no context length behind it is an error, not a guess.
	if _, err := soloSample("gpu0", plan, "", d, trace, 1.5, 0.05); err == nil || !strings.Contains(err.Error(), "context param") {
		t.Fatalf("no context param: %v", err)
	}
	if _, err := soloSample("gpu0", &v1.MemoryPlan{CacheBytes: 1, Params: map[string]string{}}, "n_ctx", d, trace, 1.5, 0.05); err == nil || !strings.Contains(err.Error(), "n_ctx") {
		t.Fatalf("missing context: %v", err)
	}
	// A descriptor without groups counts its header's parameters.
	flat, err := soloSample("gpu0", plan, "n_ctx", &v1.Descriptor{ParameterCount: 7e9}, trace, 1.5, 0.05)
	if err != nil || flat.Parameters != 7e9 {
		t.Fatalf("header parameters %+v %v", flat, err)
	}
}

// A plan's prediction for a request is its cost model re-priced at the request's prompt length,
// the largest piece there, before the learned ratio
func TestPredicted(t *testing.T) {
	plan := &v1.FormationPlan{
		PrefillCost:                []*v1.PrefillCost{{FixedSeconds: 1, SecondsPerPromptToken: 0.001}, {FixedSeconds: 0.5, SecondsPerPromptToken: 0.002}},
		ModelDecodeSecondsPerToken: 0.04,
		PrefillSeconds:             9,
		DecodeSecondsPerToken:      0.08,
	}
	ttft, tpt := predicted(plan, 1000)
	if math.Abs(ttft-2.5) > 1e-9 || tpt != 0.04 {
		t.Fatalf("predicted %v %v", ttft, tpt)
	}
	if ttft, _ := predicted(plan, 100); math.Abs(ttft-1.1) > 1e-9 {
		t.Fatalf("the other piece wins at short prompts: %v", ttft)
	}
	if ttft, tpt := predicted(&v1.FormationPlan{}, 100); ttft != 0 || tpt != 0 {
		t.Fatal("a plan without a cost model predicts nothing")
	}
}

// Only relay traces through both seats measure the relay; a request the decode seat answered
// alone is that seat's solo request, and one whose prefill failed teaches nothing
func TestRelayPath(t *testing.T) {
	if relayPath(v1.Shape_SHAPE_CHAIN, &v1.Trace{}) != pathFormation {
		t.Fatal("every other shape's trace measures the formation")
	}
	cases := map[string]learnPath{relayBoth: pathFormation, relayDecode: pathDecodeAlone, "prefill-failed": pathNone, "": pathNone}
	for mark, want := range cases {
		if got := relayPath(v1.Shape_SHAPE_RELAY, &v1.Trace{Relay: mark}); got != want {
			t.Fatalf("relay %q: %v, want %v", mark, got, want)
		}
	}
}
