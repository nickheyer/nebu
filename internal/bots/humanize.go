package bots

import (
	"math/rand/v2"
	"strings"
	"time"
	"unicode"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// How long a persona pauses before it starts typing
func preDelay(h *v1.Humanize) time.Duration {
	if !h.GetEnabled() || h.GetDelayMaxMs() == 0 {
		return 0
	}
	span := int64(h.GetDelayMaxMs() - h.GetDelayMinMs())
	ms := int64(h.GetDelayMinMs())
	if span > 0 {
		ms += rand.Int64N(span + 1)
	}
	return time.Duration(ms) * time.Millisecond
}

// How long a persona types one message, its length at the persona's speed with a little jitter, capped
func typingTime(h *v1.Humanize, text string) time.Duration {
	if !h.GetEnabled() || h.GetCharsPerSecond() <= 0 {
		return 0
	}
	seconds := float64(len([]rune(text))) / h.GetCharsPerSecond()
	seconds *= 0.85 + rand.Float64()*0.3
	d := time.Duration(seconds * float64(time.Second))
	if capped := time.Duration(h.GetMaxTypingMs()) * time.Millisecond; capped > 0 && d > capped {
		d = capped
	}
	return d
}

// Whether the persona is awake now, always without active hours
func awake(h *v1.Humanize, now time.Time) bool {
	if !h.GetEnabled() || h.GetActiveHours() == "" {
		return true
	}
	from, to, err := parseHours(h.GetActiveHours())
	if err != nil {
		return true
	}
	if tz := h.GetTimezone(); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			now = now.In(loc)
		}
	}
	minute := now.Hour()*60 + now.Minute()
	if from <= to {
		return minute >= from && minute < to
	}
	// A span past midnight
	return minute >= from || minute < to
}

// Rolls a chance from 0 to 1
func roll(chance float64) bool {
	if chance <= 0 {
		return false
	}
	if chance >= 1 {
		return true
	}
	return rand.Float64() < chance
}

// Picks one of a list, empty for an empty list
func pick(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[rand.IntN(len(list))]
}

// Makes an answer read like a person typed it: lowercase outside code, no final period
func casual(text string) string {
	if strings.Contains(text, "```") {
		return text
	}
	out := strings.ToLower(text)
	out = strings.TrimRightFunc(out, unicode.IsSpace)
	if strings.HasSuffix(out, ".") && !strings.HasSuffix(out, "..") {
		out = strings.TrimSuffix(out, ".")
	}
	return out
}

// One message per paragraph, a paragraph past the limit cut at sentences, the way a person sends a long thought
func split(text string, limit int) []string {
	if limit <= 0 || limit > discordMessageMax {
		limit = discordMessageMax
	}
	var out []string
	for _, para := range splitParagraphs(strings.TrimSpace(text)) {
		if len(para) <= limit {
			out = append(out, strings.TrimSpace(para))
			continue
		}
		out = append(out, splitLong(para, limit)...)
	}
	return out
}

// The messages an answer goes out as: paragraphs packed together up to the limit, a paragraph past it cut at
// sentences, then at spaces, then wherever it must, and never inside a code fence
func chunk(text string, limit int) []string {
	if limit <= 0 || limit > discordMessageMax {
		limit = discordMessageMax
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len(text) <= limit && !strings.Contains(text, "\n\n") {
		return []string{text}
	}
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, para := range splitParagraphs(text) {
		if cur.Len() > 0 && cur.Len()+2+len(para) > limit {
			flush()
		}
		if len(para) <= limit {
			if cur.Len() > 0 {
				cur.WriteString("\n\n")
			}
			cur.WriteString(para)
			continue
		}
		flush()
		for _, piece := range splitLong(para, limit) {
			out = append(out, piece)
		}
	}
	flush()
	return out
}

// Paragraphs of a text, a fenced code block kept whole with its neighbors' blank lines inside it
func splitParagraphs(text string) []string {
	var out []string
	var cur strings.Builder
	fenced := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if strings.TrimSpace(line) == "" && !fenced {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		if cur.Len() > 0 {
			cur.WriteString("\n")
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// Cuts one paragraph at sentence ends, then at spaces, then wherever it must
func splitLong(text string, limit int) []string {
	var out []string
	for len(text) > limit {
		cut := -1
		window := text[:limit]
		for _, sep := range []string{". ", "! ", "? ", "\n"} {
			if i := strings.LastIndex(window, sep); i > limit/3 && i+len(sep) > cut {
				cut = i + len(sep)
			}
		}
		if cut < 0 {
			if i := strings.LastIndex(window, " "); i > limit/3 {
				cut = i + 1
			}
		}
		if cut < 0 {
			cut = limit
			// Never split a multi byte character
			for cut > 0 && !isRuneStart(text[cut]) {
				cut--
			}
			if cut == 0 {
				cut = limit
			}
		}
		out = append(out, strings.TrimSpace(text[:cut]))
		text = strings.TrimSpace(text[cut:])
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
