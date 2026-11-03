package tokenizer

// The remaining text the stream holds back, for the end of an answer
func (s *Stream) Flush() string {
	text, err := s.t.Decode(s.ids[s.prefixIndex:], s.skipSpecial)
	if err != nil {
		return ""
	}
	prefix, err := s.t.Decode(s.ids[s.prefixIndex:s.readIndex], s.skipSpecial)
	if err != nil {
		return ""
	}
	out := text[len(longestCommonPrefix(text, prefix)):]
	s.prefixIndex, s.readIndex = len(s.ids), len(s.ids)
	return out
}
