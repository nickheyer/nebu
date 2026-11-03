package stub

import (
	"unicode"
	"unicode/utf8"
)

const (
	fnvOffset32 = 2166136261
	fnvPrime32  = 16777619
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

// Splits text into runs of letters and digits and single other symbols, dropping whitespace
func Tokenize(text string) []string {
	var out []string
	start := -1
	for i := 0; i < len(text); {
		r, n := utf8.DecodeRuneInString(text[i:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
		} else {
			if start >= 0 {
				out = append(out, text[start:i])
				start = -1
			}
			if !unicode.IsSpace(r) {
				out = append(out, text[i:i+n])
			}
		}
		i += n
	}
	if start >= 0 {
		out = append(out, text[start:])
	}
	return out
}

// Stable token ids from the FNV-1a hash of each token
func tokenIDs(tokens []string) []uint32 {
	ids := make([]uint32, len(tokens))
	for i, t := range tokens {
		h := uint32(fnvOffset32)
		for k := 0; k < len(t); k++ {
			h ^= uint32(t[k])
			h *= fnvPrime32
		}
		ids[i] = h
	}
	return ids
}

// Hashes each full block of ids chained onto the hash of the block before, seeded by the scope
func blockHashes(scope string, ids []uint32, size int) []uint64 {
	parent := uint64(fnvOffset64)
	for k := 0; k < len(scope); k++ {
		parent = mix64(parent, scope[k])
	}
	out := make([]uint64, 0, len(ids)/size)
	for b := 0; b+size <= len(ids); b += size {
		h := uint64(fnvOffset64)
		for k := 0; k < 8; k++ {
			h = mix64(h, byte(parent>>(8*k)))
		}
		for _, id := range ids[b : b+size] {
			for k := 0; k < 4; k++ {
				h = mix64(h, byte(id>>(8*k)))
			}
		}
		out = append(out, h)
		parent = h
	}
	return out
}

// One FNV-1a 64 round over a byte
func mix64(h uint64, b byte) uint64 {
	h ^= uint64(b)
	return h * fnvPrime64
}

// Integer division rounding up
func ceilDiv(a, b int) int { return (a + b - 1) / b }
