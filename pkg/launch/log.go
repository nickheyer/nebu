package launch

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

var (
	// CSI sequences such as colors and cursor moves, and OSC sequences such as window titles
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)
)

// Makes one line of process output safe to store and send
//
// Runtimes print progress with carriage returns, colour with escape codes, and
// sometimes bytes that are not text at all. The API carries lines as proto
// strings, which must be valid UTF-8, so every line passes through here once.
func Clean(line string) string {
	if i := strings.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}
	if strings.IndexByte(line, 0x1b) >= 0 {
		line = ansiRe.ReplaceAllString(line, "")
	}
	if !utf8.ValidString(line) {
		line = strings.ToValidUTF8(line, "�")
	}
	if strings.IndexFunc(line, isControl) < 0 {
		return line
	}
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return -1
		}
		return r
	}, line)
}

func isControl(r rune) bool {
	return (r < 0x20 && r != '\t') || r == 0x7f
}

// Ring of recent output lines with followers
type Log struct {
	mu       sync.Mutex
	capacity int
	lines    []string
	dropped  int
	closed   bool
	subs     map[chan struct{}]struct{}
}

// Builds a log keeping the last capacity lines
func NewLog(capacity int) *Log {
	return &Log{capacity: max(capacity, 1), subs: map[chan struct{}]struct{}{}}
}

// Appends one cleaned line and wakes followers
func (l *Log) Write(line string) {
	line = Clean(line)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
	if len(l.lines) > l.capacity {
		drop := len(l.lines) - l.capacity
		l.lines = l.lines[drop:]
		l.dropped += drop
	}
	l.wakeLocked()
}

// Returns the last n retained lines, all when zero
func (l *Log) Tail(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	start := 0
	if n > 0 && n < len(l.lines) {
		start = len(l.lines) - n
	}
	return append([]string(nil), l.lines[start:]...)
}

// Marks the end of output and releases followers
func (l *Log) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	l.wakeLocked()
}

func (l *Log) wakeLocked() {
	for s := range l.subs {
		select {
		case s <- struct{}{}:
		default:
		}
	}
}

// Sends the tail then new lines until close or ctx
func (l *Log) Follow(ctx context.Context, tail int, send func([]string) error) error {
	notify := make(chan struct{}, 1)
	l.mu.Lock()
	l.subs[notify] = struct{}{}
	start := 0
	if tail > 0 && tail < len(l.lines) {
		start = len(l.lines) - tail
	}
	cursor := l.dropped + start
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.subs, notify)
		l.mu.Unlock()
	}()
	for {
		l.mu.Lock()
		from := max(cursor-l.dropped, 0)
		batch := append([]string(nil), l.lines[from:]...)
		cursor = l.dropped + len(l.lines)
		closed := l.closed
		l.mu.Unlock()
		if len(batch) > 0 {
			if err := send(batch); err != nil {
				return err
			}
		}
		if closed {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notify:
		}
	}
}
