package archs

import (
	"math"
	"slices"
	"testing"

	"github.com/nickheyer/nebu/pkg/formats"
)

func params(layers, interval float64) formats.Params {
	return formats.Params{Layers: layers, HeadsKV: 8, HeadDim: 128, HeadDimV: 128, AttentionInterval: interval}
}

// A hybrid model keeps a full cache on the last layer of each attention interval and none on the
// recurrent layers between; a dense one keeps it on every layer
func TestDefaultCacheLayers(t *testing.T) {
	full := 8.0 * 256
	got, err := Default{}.CacheLayers(params(8, 4), Run{})
	if err != nil || !slices.Equal(got, []float64{0, 0, 0, full, 0, 0, 0, full}) {
		t.Fatalf("interval 4: %v %v", got, err)
	}
	got, err = Default{}.CacheLayers(params(3, 0), Run{})
	if err != nil || !slices.Equal(got, []float64{full, full, full}) {
		t.Fatalf("dense: %v %v", got, err)
	}
	if per, err := CachePerToken(Default{}, params(10, 4), Run{}); err != nil || per != 2*full {
		t.Fatalf("ten layers at interval 4 hold two full caches: %v %v", per, err)
	}
	dense := Default{}
	if _, err := dense.CacheLayers(formats.Params{Layers: 4}, Run{}); err == nil {
		t.Fatal("heads and head width are needed")
	}
}

// Sliding window families keep the full context on the last layer of each pattern and the window
// plus a batch on the others, every layer a window when the pattern is zero
func TestSlidingWindowCacheLayers(t *testing.T) {
	p := formats.Params{Layers: 12, HeadsKV: 16, HeadDim: 128, HeadDimV: 128, SlidingWindow: 1024}
	run := Run{Context: 32768, UBatch: 512}
	full := 16.0 * 256
	window := full * (1024 + 512) / 32768
	got, err := Gemma3{}.CacheLayers(p, run)
	if err != nil {
		t.Fatal(err)
	}
	for il, c := range got {
		want := window
		if (il+1)%6 == 0 {
			want = full
		}
		if math.Abs(c-want) > 1e-9 {
			t.Fatalf("layer %d holds %v, want %v", il, c, want)
		}
	}
	// The header's own pattern wins over the family's
	p.SlidingPattern = 3
	got, _ = Gemma3{}.CacheLayers(p, run)
	if got[2] != full || got[1] != window || got[5] != full {
		t.Fatalf("pattern 3: %v", got)
	}
	// Phi 3 keeps a window on every layer, and without a context every layer counts as full
	got, _ = Phi3{}.CacheLayers(formats.Params{Layers: 4, HeadsKV: 16, HeadDim: 128, HeadDimV: 128, SlidingWindow: 2048}, run)
	wide := full * (2048 + 512) / 32768
	if !slices.Equal(got, []float64{wide, wide, wide, wide}) {
		t.Fatalf("phi3: %v", got)
	}
	got, _ = Phi3{}.CacheLayers(formats.Params{Layers: 2, HeadsKV: 16, HeadDim: 128, HeadDimV: 128, SlidingWindow: 2048}, Run{})
	if !slices.Equal(got, []float64{full, full}) {
		t.Fatalf("no context: %v", got)
	}
}

// Latent attention stores the same compressed cache on every layer
func TestMLACacheLayers(t *testing.T) {
	got, err := MLA{}.CacheLayers(formats.Params{Layers: 3, KVLoraRank: 512, RopeDim: 64}, Run{})
	if err != nil || !slices.Equal(got, []float64{576, 576, 576}) {
		t.Fatalf("%v %v", got, err)
	}
}
