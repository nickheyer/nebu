package inspect

import (
	"slices"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestPlanContextsEndOnTheModelsOwn(t *testing.T) {
	defaults := []uint32{8192, 32768, 131072}
	d := &v1.Descriptor{Params: map[string]float64{"n_ctx_train": 40960}}
	if got := planContexts(defaults, d, false); !slices.Equal(got, []uint32{8192, 32768, 40960}) {
		t.Fatalf("capped %v", got)
	}
	if got := planContexts(defaults, d, true); !slices.Equal(got, defaults) {
		t.Fatalf("named contexts are kept, got %v", got)
	}
	if got := planContexts(defaults, &v1.Descriptor{}, false); !slices.Equal(got, defaults) {
		t.Fatalf("no limit keeps the defaults, got %v", got)
	}
	d.Params["n_ctx_train"] = 4096
	if got := planContexts(defaults, d, false); !slices.Equal(got, []uint32{4096}) {
		t.Fatalf("a short model plans at its limit only, got %v", got)
	}
}

func TestShardsKeepTheirDigitWidths(t *testing.T) {
	got := shards("/m/model-00002-of-000003.safetensors")
	want := []string{"/m/model-00001-of-000003.safetensors", "/m/model-00002-of-000003.safetensors", "/m/model-00003-of-000003.safetensors"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	if got := shards("/m/model-Q4_K_M.gguf"); !slices.Equal(got, []string{"/m/model-Q4_K_M.gguf"}) {
		t.Fatalf("a whole file stands alone, got %v", got)
	}
}
