package auth

import (
	"cmp"
	"slices"
	"strings"
)

// Stands in for a secret in anything read back or published, and keeps the stored secret when sent back
const Redacted = "[redacted]"

// Copies a command with every env value standing as a whole token inside a word replaced by the marker
func RedactEnv(words []string, env map[string]string) []string {
	values := make([]string, 0, len(env))
	for _, v := range env {
		if v != "" {
			values = append(values, v)
		}
	}
	slices.SortFunc(values, func(a, b string) int { return cmp.Or(len(b)-len(a), strings.Compare(a, b)) })
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = redactWord(w, values)
	}
	return out
}

// Replaces in one pass the longest value matching at each position with word boundaries on both sides
func redactWord(w string, values []string) string {
	var b strings.Builder
	for i := 0; i < len(w); {
		hit := ""
		for _, v := range values {
			if strings.HasPrefix(w[i:], v) && bounded(w, i, i+len(v), v) {
				hit = v
				break
			}
		}
		if hit == "" {
			b.WriteByte(w[i])
			i++
			continue
		}
		b.WriteString(Redacted)
		i += len(hit)
	}
	return b.String()
}

// Whether a match stands apart from letters and digits around it, as a regexp word boundary would
func bounded(w string, start, end int, v string) bool {
	left := start == 0 || !wordByte(w[start-1]) || !wordByte(v[0])
	right := end == len(w) || !wordByte(w[end]) || !wordByte(v[len(v)-1])
	return left && right
}

func wordByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}
