package mesh

import (
	"slices"
	"testing"
)

// Layers split over devices by their bytes in whole layers summing to the count, the remainder
// going to the largest fractional shares, earlier devices first on a tie
func TestSplitLayers(t *testing.T) {
	cases := []struct {
		name   string
		layers int
		bytes  []uint64
		want   []uint32
	}{
		{"one device", 30, []uint64{7}, []uint32{30}},
		{"even", 30, []uint64{10, 20}, []uint32{10, 20}},
		{"remainder to the largest fraction", 10, []uint64{1, 1, 1}, []uint32{4, 3, 3}},
		{"fractions decide", 7, []uint64{25, 35, 40}, []uint32{2, 2, 3}},
		{"fewer layers than devices", 1, []uint64{4, 4}, []uint32{1, 0}},
		{"no bytes anywhere", 5, []uint64{0, 0}, []uint32{5, 0}},
		{"a device without bytes takes none", 5, []uint64{0, 8}, []uint32{0, 5}},
		{"no layers", 0, []uint64{1, 2}, []uint32{0, 0}},
		{"no devices", 4, nil, []uint32{}},
	}
	for _, c := range cases {
		got := splitLayers(c.layers, c.bytes)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		var sum uint32
		for _, n := range got {
			sum += n
		}
		if len(c.bytes) > 0 && c.layers > 0 && int(sum) != c.layers {
			t.Errorf("%s: %d layers placed of %d", c.name, sum, c.layers)
		}
	}
}
