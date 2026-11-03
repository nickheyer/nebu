package tokenizer

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Rewrites the token strings of an answer on their way to text
type decoder interface {
	decodeChain(tokens []string) []string
}

type decoderSequence []decoder

func (s decoderSequence) decodeChain(tokens []string) []string {
	for _, d := range s {
		tokens = d.decodeChain(tokens)
	}
	return tokens
}

type byteLevelDecoder struct{}

func (byteLevelDecoder) decodeChain(tokens []string) []string {
	var all []byte
	for _, t := range tokens {
		all = append(all, fromByteLevel(t)...)
	}
	return []string{lossyUTF8(all)}
}

// Decodes bytes as UTF-8 with one replacement character per maximal ill formed subpart, as Rust does
func lossyUTF8(b []byte) string {
	var out strings.Builder
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || n > 1 {
			out.Write(b[i : i+n])
			i += n
			continue
		}
		out.WriteString("�")
		i += illFormedLen(b[i:])
	}
	return out.String()
}

// The length of the maximal subpart of an ill formed sequence at the start of the bytes
func illFormedLen(b []byte) int {
	lead := b[0]
	var need int
	lo, hi := byte(0x80), byte(0xBF)
	switch {
	case lead >= 0xC2 && lead <= 0xDF:
		need = 1
	case lead >= 0xE0 && lead <= 0xEF:
		need = 2
		if lead == 0xE0 {
			lo = 0xA0
		} else if lead == 0xED {
			hi = 0x9F
		}
	case lead >= 0xF0 && lead <= 0xF4:
		need = 3
		if lead == 0xF0 {
			lo = 0x90
		} else if lead == 0xF4 {
			hi = 0x8F
		}
	default:
		return 1
	}
	n := 1
	for k := 1; k <= need && k < len(b); k++ {
		if b[k] < lo || b[k] > hi {
			break
		}
		lo, hi = 0x80, 0xBF
		n++
	}
	return n
}

type wordPieceDecoder struct {
	prefix  string
	cleanup bool
}

// Undoes the spacing BERT tokenization put around punctuation and contractions
func cleanupText(s string) string {
	r := strings.NewReplacer(" .", ".", " ?", "?", " !", "!", " ,", ",", " ' ", "'", " n't", "n't", " 'm", "'m", " do not", " don't", " 's", "'s", " 've", "'ve", " 're", "'re")
	return r.Replace(s)
}

func (d wordPieceDecoder) decodeChain(tokens []string) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		if i != 0 {
			if rest, ok := strings.CutPrefix(t, d.prefix); ok {
				t = rest
			} else {
				t = " " + t
			}
		}
		if d.cleanup {
			t = cleanupText(t)
		}
		out[i] = t
	}
	return out
}

type metaspaceDecoder struct {
	replacement rune
	prepend     string
}

// Turns replacements into spaces, those of the first token dropped unless the scheme is never
func (d metaspaceDecoder) decodeChain(tokens []string) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		var b strings.Builder
		for _, r := range t {
			if r == d.replacement {
				if i == 0 && d.prepend != "never" {
					continue
				}
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(r)
		}
		out[i] = b.String()
	}
	return out
}

type bpeDecoder struct{ suffix string }

func (d bpeDecoder) decodeChain(tokens []string) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		rep := " "
		if i == len(tokens)-1 {
			rep = ""
		}
		out[i] = strings.ReplaceAll(t, d.suffix, rep)
	}
	return out
}

type byteFallbackDecoder struct{}

// Joins runs of byte tokens into text, an invalid run giving one replacement character per byte
func (byteFallbackDecoder) decodeChain(tokens []string) []string {
	var out []string
	var pending []byte
	flush := func() {
		if len(pending) == 0 {
			return
		}
		s := string(pending)
		if strings.ToValidUTF8(s, "") == s {
			out = append(out, s)
		} else {
			for range pending {
				out = append(out, "�")
			}
		}
		pending = pending[:0]
	}
	for _, t := range tokens {
		if len(t) == 6 && strings.HasPrefix(t, "<0x") && strings.HasSuffix(t, ">") {
			if b, err := strconv.ParseUint(t[3:5], 16, 8); err == nil {
				pending = append(pending, byte(b))
				continue
			}
		}
		flush()
		out = append(out, t)
	}
	flush()
	return out
}

type fuseDecoder struct{}

func (fuseDecoder) decodeChain(tokens []string) []string {
	return []string{strings.Join(tokens, "")}
}

type stripDecoder struct {
	content     rune
	start, stop int
}

// Cuts up to start leading and stop trailing copies of the content from every token
func (d stripDecoder) decodeChain(tokens []string) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		chars := []rune(t)
		from := 0
		for k := 0; k < d.start && k < len(chars) && chars[k] == d.content; k++ {
			from = k + 1
		}
		to := len(chars)
		for k := 0; k < d.stop && len(chars)-k-1 >= 0 && chars[len(chars)-k-1] == d.content; k++ {
			to = len(chars) - k - 1
		}
		if to < from {
			to = from
		}
		out[i] = string(chars[from:to])
	}
	return out
}

type replaceDecoder struct{ replace }

func (d replaceDecoder) decodeChain(tokens []string) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = d.normalize(t, &normState{offset: 1})
	}
	return out
}

type ctcDecoder struct {
	pad, delimiter string
	cleanup        bool
}

// Collapses repeated tokens, drops padding and turns the word delimiter into spaces
func (d ctcDecoder) decodeChain(tokens []string) []string {
	var out []string
	var last string
	for i, t := range tokens {
		if i > 0 && t == last {
			continue
		}
		last = t
		s := strings.ReplaceAll(t, d.pad, "")
		if d.cleanup {
			s = strings.ReplaceAll(cleanupText(s), d.delimiter, " ")
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
