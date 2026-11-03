package tokenizer

import (
	"unicode"
	"unicode/utf8"
)

// Grapheme cluster break classes of UAX 29
const (
	gbOther = iota
	gbCR
	gbLF
	gbControl
	gbExtend
	gbZWJ
	gbRI
	gbPrepend
	gbSpacingMark
	gbL
	gbV
	gbT
	gbLV
	gbLVT
)

// The grapheme break class of a rune
func graphemeClass(r rune) int {
	switch {
	case r == '\r':
		return gbCR
	case r == '\n':
		return gbLF
	case r == 0x200D:
		return gbZWJ
	case r >= 0x1F1E6 && r <= 0x1F1FF:
		return gbRI
	case (r >= 0x1100 && r <= 0x115F) || (r >= 0xA960 && r <= 0xA97C):
		return gbL
	case (r >= 0x1160 && r <= 0x11A7) || (r >= 0xD7B0 && r <= 0xD7C6):
		return gbV
	case (r >= 0x11A8 && r <= 0x11FF) || (r >= 0xD7CB && r <= 0xD7FB):
		return gbT
	case r >= 0xAC00 && r <= 0xD7A3:
		if (r-0xAC00)%28 == 0 {
			return gbLV
		}
		return gbLVT
	case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Other_Grapheme_Extend, r):
		return gbExtend
	case unicode.Is(unicode.Prepended_Concatenation_Mark, r):
		return gbPrepend
	case unicode.Is(unicode.Mc, r):
		return gbSpacingMark
	case unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r):
		return gbControl
	}
	return gbOther
}

// Whether a cluster boundary falls between two classes, the pair count tracking regional indicators
func graphemeBreak(prev, next int, riRun int) bool {
	switch {
	case prev == gbCR && next == gbLF:
		return false
	case prev == gbControl || prev == gbCR || prev == gbLF:
		return true
	case next == gbControl || next == gbCR || next == gbLF:
		return true
	case prev == gbL && (next == gbL || next == gbV || next == gbLV || next == gbLVT):
		return false
	case (prev == gbLV || prev == gbV) && (next == gbV || next == gbT):
		return false
	case (prev == gbLVT || prev == gbT) && next == gbT:
		return false
	case next == gbExtend || next == gbZWJ || next == gbSpacingMark:
		return false
	case prev == gbPrepend:
		return false
	case prev == gbRI && next == gbRI:
		return riRun%2 == 0
	}
	return true
}

// Calls fn with the byte range of every extended grapheme cluster of the text
func graphemes(s string, fn func(start, end int)) {
	if s == "" {
		return
	}
	start := 0
	prev := -1
	riRun := 0
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		class := graphemeClass(r)
		if prev >= 0 && graphemeBreak(prev, class, riRun) {
			fn(start, i)
			start = i
		}
		if class == gbRI {
			riRun++
		} else {
			riRun = 0
		}
		prev = class
		i += n
	}
	fn(start, len(s))
}
