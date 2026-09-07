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
