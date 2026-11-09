package tokenizer

import "strings"

// Decodes ids as they arrive, holding text back while its last character is still incomplete
type Stream struct {
	t           *Tokenizer
	skipSpecial bool
	ids         []uint32
	// The window decoded each step starts at prefixIndex, its text up to readIndex already emitted
	prefixIndex int
	readIndex   int
}

// Starts an incremental decode
func (t *Tokenizer) NewStream(skipSpecialTokens bool) *Stream {
	return &Stream{t: t, skipSpecial: skipSpecialTokens}
}

// Adds one id and returns the text it completed, empty while the text is still partial
func (s *Stream) Step(id uint32) (string, error) {
	s.ids = append(s.ids, id)
	text, err := s.t.Decode(s.ids[s.prefixIndex:], s.skipSpecial)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(text, "�") {
		return "", nil
	}
	prefix, err := s.t.Decode(s.ids[s.prefixIndex:s.readIndex], s.skipSpecial)
	if err != nil {
		return "", err
	}
	out := text[len(longestCommonPrefix(text, prefix)):]
	if out == "" {
		return "", nil
	}
	s.prefixIndex = s.readIndex
	s.readIndex = len(s.ids)
	return out, nil
}

func longestCommonPrefix(a, b string) string {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return a[:n]
}
