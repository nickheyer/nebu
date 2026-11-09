package tokenizer

// The GPT-2 byte to printable character table and its inverse
var (
	byteChars [256]rune
	charBytes [512]int16
)

func init() {
	for i := range charBytes {
		charBytes[i] = -1
	}
	n := 0
	for b := 0; b < 256; b++ {
		var r rune
		if (b >= '!' && b <= '~') || (b >= 0xA1 && b <= 0xAC) || (b >= 0xAE && b <= 0xFF) {
			r = rune(b)
		} else {
			r = rune(256 + n)
			n++
		}
		byteChars[b] = r
		charBytes[r] = int16(b)
	}
}

// Maps every byte of the text onto its byte level character
func toByteLevel(s string) string {
	out := make([]rune, 0, len(s))
	for i := 0; i < len(s); i++ {
		out = append(out, byteChars[s[i]])
	}
	return string(out)
}

// Maps byte level characters back to bytes, characters outside the alphabet kept as their own bytes
func fromByteLevel(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r < 512 && charBytes[r] >= 0 {
			out = append(out, byte(charBytes[r]))
			continue
		}
		out = append(out, string(r)...)
	}
	return out
}
