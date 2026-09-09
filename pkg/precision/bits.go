package precision

// Words by bits per weight, the first width at or below the measured one winning
type Bits struct{}

var levels = []Level{
	{Bits: 32, Label: "32-bit float", Blurb: "The original weights at full size. Largest of all, nothing lost.", Quality: 5},
	{Bits: 16, Label: "16-bit", Blurb: "Full quality, the precision the model was trained at. Needs the most memory.", Quality: 5},
	{Bits: 8, Label: "8-bit", Blurb: "Near lossless. Half the size of 16-bit with quality most people cannot tell apart.", Quality: 4},
	{Bits: 6, Label: "6-bit", Blurb: "Very close to full quality at about a third of the 16-bit size.", Quality: 4},
	{Bits: 5, Label: "5-bit", Blurb: "Small quality loss, slightly smaller than 6-bit.", Quality: 3},
	{Bits: 4, Label: "4-bit", Blurb: "The usual choice. About a quarter of the 16-bit size with modest quality loss.", Quality: 3},
	{Bits: 3, Label: "3-bit", Blurb: "Noticeably lower quality. For when 4-bit does not fit.", Quality: 2},
	{Bits: 2, Label: "2-bit", Blurb: "Heavy quality loss. A last resort for tight memory.", Quality: 1},
	{Bits: 1, Label: "1-bit", Blurb: "Extreme compression. Expect degraded answers.", Quality: 1},
	{Bits: 0, Label: "Unknown precision", Blurb: "The headers did not say how the weights are stored.", Quality: 0},
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
