package bots

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestChunkSplitsAtParagraphsSentencesAndLimits(t *testing.T) {
	if got := chunk("short", 2000); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short %v", got)
	}
	got := split("one\n\ntwo\n\nthree", 2000)
	if len(got) != 3 || got[1] != "two" {
		t.Fatalf("paragraphs %v", got)
	}
	if got := chunk("one\n\ntwo\n\nthree", 2000); len(got) != 1 || got[0] != "one\n\ntwo\n\nthree" {
		t.Fatalf("packed %v", got)
	}
	if got := chunk("one\n\ntwo", 6); len(got) != 2 || got[1] != "two" {
		t.Fatalf("packing past the limit %v", got)
	}
	long := strings.Repeat("Sentence one is here. ", 60)
	got = chunk(long, 300)
	for _, c := range got {
		if len(c) > 300 || !strings.HasSuffix(c, ".") {
			t.Fatalf("sentence cut wrong: %q", c)
		}
	}
	if strings.Join(got, " ") != strings.TrimSpace(long) {
		t.Fatal("text lost while chunking")
	}
	fenced := "before\n\n```go\nfunc main() {\n\n}\n```\n\nafter"
	got = split(fenced, 2000)
	if len(got) != 3 || !strings.Contains(got[1], "func main() {\n\n}") {
		t.Fatalf("fence split %v", got)
	}
	hard := strings.Repeat("x", 5000)
	got = chunk(hard, 2000)
	if len(got) != 3 || len(got[0]) != 2000 || len(got[2]) != 1000 {
		t.Fatalf("hard cut %d %d", len(got), len(got[0]))
	}
	multi := strings.Repeat("é", 1500)
	for _, c := range chunk(multi, 2000) {
		if !strings.HasPrefix(c, "é") || strings.ContainsRune(c, '�') {
			t.Fatal("split inside a rune")
		}
	}
}

func TestCasual(t *testing.T) {
	if casual("Hello There.") != "hello there" {
		t.Fatal(casual("Hello There."))
	}
	if casual("Wait...") != "wait..." {
		t.Fatal(casual("Wait..."))
	}
	code := "Use `Foo`:\n```go\nFoo()\n```"
	if casual(code) != code {
		t.Fatal("code changed")
	}
}

func TestAwakeAndTiming(t *testing.T) {
	h := &v1.Humanize{Enabled: true, ActiveHours: "09:00-17:00", Timezone: "UTC"}
	at := func(hour int) time.Time { return time.Date(2026, 1, 5, hour, 30, 0, 0, time.UTC) }
	if !awake(h, at(10)) || awake(h, at(8)) || awake(h, at(17)) {
		t.Fatal("day span")
	}
	h.ActiveHours = "22:00-06:00"
	if !awake(h, at(23)) || !awake(h, at(2)) || awake(h, at(12)) {
		t.Fatal("night span")
	}
	h.Enabled = false
	if !awake(h, at(12)) {
		t.Fatal("humanize off must always be awake")
	}
	h = &v1.Humanize{Enabled: true, CharsPerSecond: 10, MaxTypingMs: 500}
	if d := typingTime(h, strings.Repeat("a", 1000)); d != 500*time.Millisecond {
		t.Fatalf("cap %v", d)
	}
	h.MaxTypingMs = 0
	d := typingTime(h, "abcdefghij")
	if d < 800*time.Millisecond || d > 1200*time.Millisecond {
		t.Fatalf("ten chars at ten a second took %v", d)
	}
	h.DelayMinMs, h.DelayMaxMs = 100, 200
	for i := 0; i < 20; i++ {
		if d := preDelay(h); d < 100*time.Millisecond || d > 200*time.Millisecond {
			t.Fatalf("delay %v", d)
		}
	}
	if preDelay(&v1.Humanize{}) != 0 || typingTime(&v1.Humanize{}, "x") != 0 {
		t.Fatal("humanize off must not wait")
	}
}

func TestHasWord(t *testing.T) {
	if !hasWord("hey nova, hi", "nova") || hasWord("supernova", "nova") || hasWord("novas", "nova") || !hasWord("nova", "nova") {
		t.Fatal("word boundaries")
	}
}

func TestMergeAndTrimTurns(t *testing.T) {
	turns := mergeTurns([]turn{{role: "user", text: "a"}, {role: "user", text: "b"}, {role: "assistant", text: "c"}, {role: "user", text: "d"}})
	if len(turns) != 3 || turns[0].text != "a\nb" {
		t.Fatalf("merge %v", turns)
	}
	trimmed := trimTurns(turns, 1)
	if len(trimmed) != 1 || trimmed[0].text != "d" {
		t.Fatalf("trim %v", trimmed)
	}
}

func TestThin(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}
	if got := thin(items, 3); len(got) != 3 || got[0] != "a" || got[2] != "e" {
		t.Fatalf("thin %v", got)
	}
	if got := thin(items, 1); len(got) != 1 || got[0] != "c" {
		t.Fatalf("thin one %v", got)
	}
	if got := thin(items, 10); len(got) != 5 {
		t.Fatal("thin more than there are")
	}
}
