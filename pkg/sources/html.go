package sources

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

var (
	tagRe   = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe = regexp.MustCompile(`\s+`)
	dropRe  = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</\s*(script|style)\s*>`)
)

// Turns HTML into plain text, collapsing whitespace
func StripTags(s string) string {
	s = dropRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// Shortens text to at most n runes on a word boundary
func Excerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	if i := strings.LastIndexAny(cut, " \n\t"); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

// Parses a compact count such as 1.4M or 12K into a number
func ParseCount(s string) uint64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return 0
	}
	mult := 1.0
	switch s[len(s)-1] {
	case 'K', 'k':
		mult, s = 1e3, s[:len(s)-1]
	case 'M', 'm':
		mult, s = 1e6, s[:len(s)-1]
	case 'B', 'b':
		mult, s = 1e9, s[:len(s)-1]
	}
	var f float64
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			return 0
		}
	}
	if _, err := fmt.Sscan(s, &f); err != nil {
		return 0
	}
	return uint64(f*mult + 0.5)
}

// Parses a human size such as 1.3GB into bytes, decimal units as sites print them
func ParseSize(s string) uint64 {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		mult   float64
	}{{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"B", 1}}
	upper := strings.ToUpper(s)
	for _, u := range units {
		if strings.HasSuffix(upper, u.suffix) {
			var f float64
			if _, err := fmt.Sscan(strings.TrimSpace(upper[:len(upper)-len(u.suffix)]), &f); err != nil {
				return 0
			}
			return uint64(f*u.mult + 0.5)
		}
	}
	return 0
}
