package launch

import (
	"bufio"
	"bytes"
	"io"
)

// Longest line kept whole, a longer run without a break arriving in pieces of this size
const MaxLine = 64 << 20

// Splits at LF, CR, and CRLF, counting CRLF once across reads and skipping empty segments ending in a lone CR
func LineSplitter() bufio.SplitFunc {
	afterCR := false
	return func(data []byte, atEOF bool) (int, []byte, error) {
		start := 0
		for {
			if afterCR && start < len(data) {
				afterCR = false
				if data[start] == '\n' {
					start++
					continue
				}
			}
			i := bytes.IndexAny(data[start:], "\r\n")
			if i < 0 {
				rest := data[start:]
				if len(rest) >= MaxLine {
					return start + MaxLine, rest[:MaxLine], nil
				}
				if atEOF && len(rest) > 0 {
					return len(data), rest, nil
				}
				return start, nil, nil
			}
			end := start + i
			if data[end] == '\n' {
				return end + 1, data[start:end], nil
			}
			if i > 0 {
				afterCR = true
				return end + 1, data[start:end], nil
			}
			// An empty segment ending at CR is a blank line only when LF follows, so look one byte ahead
			switch {
			case end+1 < len(data) && data[end+1] == '\n':
				return end + 2, data[start:end], nil
			case end+1 < len(data) || atEOF:
				start = end + 1
			default:
				return start, nil, nil
			}
		}
	}
}

// Calls each with every line of r until EOF, split as LineSplitter splits
func Lines(r io.Reader, each func(line string)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), MaxLine)
	scanner.Split(LineSplitter())
	for scanner.Scan() {
		each(scanner.Text())
	}
	return scanner.Err()
}

// Cuts a byte stream into lines as bytes arrive, keeping the unfinished tail
type lineStream struct {
	split   bufio.SplitFunc
	pending []byte
}

func newLineStream() *lineStream { return &lineStream{split: LineSplitter()} }

// Adds bytes and emits every line they complete
func (s *lineStream) feed(data []byte, emit func(string)) {
	s.pending = append(s.pending, data...)
	s.cut(false, emit)
}

// Emits the unfinished tail as the last line
func (s *lineStream) flush(emit func(string)) { s.cut(true, emit) }

func (s *lineStream) cut(atEOF bool, emit func(string)) {
	off := 0
	for off < len(s.pending) {
		advance, token, _ := s.split(s.pending[off:], atEOF)
		off += advance
		if token != nil {
			emit(string(token))
		}
		if advance == 0 {
			break
		}
	}
	s.pending = s.pending[:copy(s.pending, s.pending[off:])]
}
