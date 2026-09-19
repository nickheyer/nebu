package precision

// Words by bits per weight, the first width at or below the measured one winning
type Bits struct{}

var levels = []Level{
	{Bits: 32, Label: "32-bit float", Blurb: "32-bit floating point weights.", Quality: 5},
	{Bits: 16, Label: "16-bit", Blurb: "16-bit weights without quantization.", Quality: 5},
	{Bits: 8, Label: "8-bit", Blurb: "About half the size of 16-bit weights, with low quantization loss.", Quality: 4},
	{Bits: 6, Label: "6-bit", Blurb: "About 38% of the 16-bit size, with low quantization loss.", Quality: 4},
	{Bits: 5, Label: "5-bit", Blurb: "Smaller than 6-bit, with some quantization loss.", Quality: 3},
	{Bits: 4, Label: "4-bit", Blurb: "About a quarter of the 16-bit size, with some quantization loss.", Quality: 3},
	{Bits: 3, Label: "3-bit", Blurb: "Less memory than 4-bit, with greater quantization loss.", Quality: 2},
	{Bits: 2, Label: "2-bit", Blurb: "Very low memory use with substantial quantization loss.", Quality: 1},
	{Bits: 1, Label: "1-bit", Blurb: "Minimum memory use with substantial quantization loss.", Quality: 1},
	{Bits: 0, Label: "Unknown precision", Blurb: "Weight precision is absent from the headers.", Quality: 0},
}

func (Bits) Levels() []Level { return levels }

func (Bits) Level(bits uint32) Level {
	for _, l := range levels {
		if bits >= l.Bits {
			return l
		}
	}
	return levels[len(levels)-1]
}
