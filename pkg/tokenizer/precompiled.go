package tokenizer

import (
	"encoding/binary"
	"errors"
	"unicode/utf8"
)

// A SentencePiece precompiled charsmap, a double array trie over bytes whose leaves index replacement text
type precompiled struct {
	array      []uint32
	normalized string
}

// Parses the charsmap bytes, a little endian trie size, the trie and the NUL separated replacements
func newPrecompiled(blob []byte) (*precompiled, error) {
	if len(blob) < 4 {
		return nil, errors.New("precompiled charsmap is shorter than its header")
	}
	size := int(binary.LittleEndian.Uint32(blob))
	if size%4 != 0 || 4+size > len(blob) {
		return nil, errors.New("precompiled charsmap trie size does not fit the blob")
	}
	array := make([]uint32, size/4)
	for i := range array {
		array[i] = binary.LittleEndian.Uint32(blob[4+4*i:])
	}
	normalized := blob[4+size:]
	if !utf8.Valid(normalized) {
		return nil, errors.New("precompiled charsmap replacements are not UTF-8")
	}
	return &precompiled{array: array, normalized: string(normalized)}, nil
}

func unitHasLeaf(u uint32) bool { return (u>>8)&1 == 1 }
func unitValue(u uint32) int    { return int(u & ((1 << 31) - 1)) }
func unitLabel(u uint32) uint32 { return u & ((1 << 31) | 0xFF) }
func unitOffset(u uint32) uint32 {
	return (u >> 10) << ((u & (1 << 9)) >> 6)
}

// The shortest key prefix stored in the trie, as its replacement index, or -1
func (p *precompiled) shortestPrefix(key []byte) int {
	pos := uint32(0)
	unit := p.array[pos]
	pos ^= unitOffset(unit)
	for _, c := range key {
		if c == 0 {
			break
		}
		pos ^= uint32(c)
		if int(pos) >= len(p.array) {
			return -1
		}
		unit = p.array[pos]
		if unitLabel(unit) != uint32(c) {
			return -1
		}
		pos ^= unitOffset(unit)
		if unitHasLeaf(unit) {
			if int(pos) >= len(p.array) {
				return -1
			}
			return unitValue(p.array[pos])
		}
	}
	return -1
}

// The replacement of a chunk, ok false when the trie holds no prefix of it
func (p *precompiled) transform(chunk string) (string, bool) {
	idx := p.shortestPrefix([]byte(chunk))
	if idx < 0 || idx > len(p.normalized) {
		return "", false
	}
	end := idx
	for end < len(p.normalized) && p.normalized[end] != 0 {
		end++
	}
	return p.normalized[idx:end], true
}

// Rewrites each grapheme under six bytes whole when mapped, otherwise each of its characters
func (p *precompiled) normalize(s string, st *normState) string {
	var out []byte
	changed := false
	graphemes(s, func(start, end int) {
		g := s[start:end]
		if len(g) < 6 {
			if rep, ok := p.transform(g); ok {
				if !changed {
					out = append(out, s[:start]...)
					changed = true
				}
				if start == 0 && rep == "" {
					st.originZero = false
				}
				out = append(out, rep...)
				return
			}
		}
		for i := 0; i < len(g); {
			_, n := utf8.DecodeRuneInString(g[i:])
			if rep, ok := p.transform(g[i : i+n]); ok {
				if !changed {
					out = append(out, s[:start+i]...)
					changed = true
				}
				if start+i == 0 && rep == "" {
					st.originZero = false
				}
				out = append(out, rep...)
			} else if changed {
				out = append(out, g[i:i+n]...)
			}
			i += n
		}
	})
	if !changed {
		return s
	}
	return string(out)
}
