package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

	"github.com/nickheyer/nebu/internal/chat"
)

const (
	// Answers whose tool rounds are kept, the oldest leaving past either bound
	roundsMax    = 4096
	roundsBudget = 128 << 20
)

// Tool rounds of recent answers, kept so an echoed answer gets them back
type roundStore struct {
	mu      sync.Mutex
	entries map[string]*roundEntry
	order   []string
	held    int
}

// An answer's earlier rounds, its final turn with ran calls before held ones, and that round's results
type roundEntry struct {
	before   []chat.Message
	final    chat.Message
	after    []chat.Message
	heldFrom int
	size     int
}

func newRoundStore() *roundStore { return &roundStore{entries: map[string]*roundEntry{}} }

// The key an echoed answer makes from the route, the turn before it, its text, and its held calls
func roundKey(route string, prev *chat.Message, text string, calls []chat.ToolCall) string {
	h := sha256.New()
	h.Write([]byte(route))
	h.Write([]byte{0})
	if prev != nil {
		h.Write([]byte(prev.Role))
		h.Write([]byte{0})
		h.Write([]byte(strings.TrimSpace(chat.TextOf(prev.Parts))))
	}
	h.Write([]byte{0})
	h.Write([]byte(strings.TrimSpace(text)))
	for _, c := range calls {
		h.Write([]byte{0})
		h.Write([]byte(c.Name))
		h.Write([]byte{0})
		h.Write([]byte(canonicalArgs(c.Args)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Arguments with their keys sorted and spacing dropped, so every protocol's echo of a call reads the same
func canonicalArgs(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		if out, err := json.Marshal(v); err == nil {
			return string(out)
		}
	}
	return strings.TrimSpace(s)
}

// Bytes a turn holds in text, thinking, and arguments
func turnSize(m chat.Message) int {
	n := 0
	for _, p := range m.Parts {
		n += len(p.Text) + len(p.Data)
	}
	for _, c := range m.ToolCalls {
		n += len(c.Name) + len(c.Args)
	}
	return n
}

// Keeps an answer's turns, dropping the oldest answers past the count and byte bounds
func (s *roundStore) put(key string, before []chat.Message, final chat.Message, after []chat.Message, heldFrom int) {
	e := &roundEntry{before: before, final: final, after: after, heldFrom: heldFrom, size: turnSize(final)}
	for _, m := range before {
		e.size += turnSize(m)
	}
	for _, m := range after {
		e.size += turnSize(m)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.entries[key]; ok {
		s.held -= old.size
	} else {
		s.order = append(s.order, key)
	}
	s.entries[key] = e
	s.held += e.size
	for len(s.order) > 1 && (len(s.order) > roundsMax || s.held > roundsBudget) {
		oldest := s.order[0]
		s.order = s.order[1:]
		if gone, ok := s.entries[oldest]; ok {
			s.held -= gone.size
			delete(s.entries, oldest)
		}
	}
}

func (s *roundStore) get(key string) (*roundEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	return e, ok
}

// Puts the tool rounds back behind every assistant turn that echoes an answer they led to, answering whether any did
func (s *roundStore) splice(route string, msgs []chat.Message) ([]chat.Message, bool) {
	var out []chat.Message
	changed := false
	for i, m := range msgs {
		if m.Role != "assistant" {
			out = append(out, m)
			continue
		}
		var prev *chat.Message
		if i > 0 {
			prev = &msgs[i-1]
		}
		e, ok := s.get(roundKey(route, prev, chat.TextOf(m.Parts), m.ToolCalls))
		if !ok {
			out = append(out, m)
			continue
		}
		changed = true
		out = append(out, e.before...)
		final := e.final
		final.ToolCalls = append(append([]chat.ToolCall(nil), final.ToolCalls[:e.heldFrom]...), m.ToolCalls...)
		final.Extra = m.Extra
		out = append(out, final)
		out = append(out, e.after...)
	}
	return out, changed
}
