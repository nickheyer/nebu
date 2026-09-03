// Package triage matches runtime output against known failure patterns.
package triage

import (
	"fmt"
	"regexp"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

type rule struct {
	spec *v1.TriageRule
	re   *regexp.Regexp
}

// Compiled triage sets keyed by spec id
type Matcher struct {
	sets map[string][]rule
}

// Compiles every triage spec
func New(specs []*v1.TriageSpec) (*Matcher, error) {
	m := &Matcher{sets: map[string][]rule{}}
	for _, s := range specs {
		for _, r := range s.GetRules() {
			re, err := regexp.Compile(r.GetMatch())
			if err != nil {
				return nil, fmt.Errorf("triage %s rule %s: %w", s.GetId(), r.GetId(), err)
			}
			m.sets[s.GetId()] = append(m.sets[s.GetId()], rule{spec: r, re: re})
		}
	}
	return m, nil
}

// Returns the first hit per rule across the given sets
func (m *Matcher) Scan(setIDs []string, lines []string) []*v1.TriageHit {
	var hits []*v1.TriageHit
	for _, id := range setIDs {
		for _, r := range m.sets[id] {
			for _, line := range lines {
				idx := r.re.FindStringSubmatchIndex(line)
				if idx == nil {
					continue
				}
				expand := func(s string) string { return string(r.re.ExpandString(nil, s, line, idx)) }
				hits = append(hits, &v1.TriageHit{
					Id:      r.spec.GetId(),
					Summary: expand(r.spec.GetSummary()),
					Hint:    expand(r.spec.GetHint()),
					Line:    line,
					Fix:     r.spec.GetFix(),
				})
				break
			}
		}
	}
	return hits
}
