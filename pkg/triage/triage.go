// Package triage reads a runtime's output for the failures people meet, one file of patterns per runtime.
package triage

import (
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// One failure a runtime prints, what it means, and what to do
type Rule struct {
	ID      string
	Summary string
	Hint    string
	// Params that fix it, applied to the next run
	Fix map[string]string
	// Whether a log line is this failure, with any names it carries filled into the summary and hint
	Match func(line string) (map[string]string, bool)
}

// The failures one runtime's output can show
type Set interface {
	ID() string
	Description() string
	Rules() []Rule
}

// Scans lines for every rule of the sets, the first line matching a rule making its hit
func Scan(sets []Set, lines []string) []*v1.TriageHit {
	var hits []*v1.TriageHit
	for _, set := range sets {
		for _, r := range set.Rules() {
			for _, line := range lines {
				names, ok := r.Match(line)
				if !ok {
					continue
				}
				hits = append(hits, &v1.TriageHit{
					Id:      r.ID,
					Summary: fill(r.Summary, names),
					Hint:    fill(r.Hint, names),
					Line:    line,
					Fix:     r.Fix,
				})
				break
			}
		}
	}
	return hits
}

// Replaces every ${name} with what the match found
func fill(s string, names map[string]string) string {
	for k, v := range names {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
	}
	return s
}

// A rule that matches when a line holds any of the phrases, case insensitively
func anyOf(phrases ...string) func(string) (map[string]string, bool) {
	return func(line string) (map[string]string, bool) {
		lower := strings.ToLower(line)
		for _, p := range phrases {
			if strings.Contains(lower, strings.ToLower(p)) {
				return nil, true
			}
		}
		return nil, false
	}
}

// A rule that matches when a line holds every phrase, case insensitively
func allOf(phrases ...string) func(string) (map[string]string, bool) {
	return func(line string) (map[string]string, bool) {
		lower := strings.ToLower(line)
		for _, p := range phrases {
			if !strings.Contains(lower, strings.ToLower(p)) {
				return nil, false
			}
		}
		return nil, true
	}
}

// Whether a line holds a phrase exactly as written
func contains(line, phrase string) bool { return strings.Contains(line, phrase) }

// The text between a phrase and the next closing quote, for a rule that names what the runtime rejected
func quotedAfter(line, phrase, quote string) (string, bool) {
	i := strings.Index(line, phrase)
	if i < 0 {
		return "", false
	}
	rest := line[i+len(phrase):]
	end := strings.Index(rest, quote)
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}
