package precision

import "testing"

// A width lands on the first level at or below it, unknown widths on the last
func TestLevel(t *testing.T) {
	var scale Scale = Bits{}
	for _, tc := range []struct {
		bits    uint32
		label   string
		quality uint32
	}{
		{32, "32-bit float", 5},
		{64, "32-bit float", 5},
		{16, "16-bit", 5},
		{17, "16-bit", 5},
		{8, "8-bit", 4},
		{7, "6-bit", 4},
		{6, "6-bit", 4},
		{5, "5-bit", 3},
		{4, "4-bit", 3},
		{3, "3-bit", 2},
		{2, "2-bit", 1},
		{1, "1-bit", 1},
		{0, "Unknown precision", 0},
	} {
		got := scale.Level(tc.bits)
		if got.Label != tc.label || got.Quality != tc.quality || got.Blurb == "" {
			t.Fatalf("Level(%d) = %+v, want %s quality %d", tc.bits, got, tc.label, tc.quality)
		}
	}
}

// Levels run from the widest to the unknown, each width below the one before
func TestLevelsOrdered(t *testing.T) {
	levels := Bits{}.Levels()
	if len(levels) != 10 || levels[0].Bits != 32 || levels[len(levels)-1].Bits != 0 {
		t.Fatalf("levels %+v", levels)
	}
	for i := 1; i < len(levels); i++ {
		if levels[i].Bits >= levels[i-1].Bits || levels[i].Quality > levels[i-1].Quality {
			t.Fatalf("level %d %+v does not fall below %+v", i, levels[i], levels[i-1])
		}
	}
}
