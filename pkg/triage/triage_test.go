package triage

import (
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestScan(t *testing.T) {
	m, err := New([]*v1.TriageSpec{{Id: "x", Rules: []*v1.TriageRule{
		{Id: "arch", Match: `unknown model architecture: '(?P<arch>[^']+)'`, Summary: "no ${arch}", Hint: "build for ${arch}", Fix: map[string]string{"a": "b"}},
		{Id: "oom", Match: `(?i)out of memory`, Summary: "oom", Hint: "lower ctx"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{"loading", "unknown model architecture: 'qwen9'", "CUDA out of memory", "unknown model architecture: 'other'"}
	hits := m.Scan([]string{"x", "missing"}, lines)
	if len(hits) != 2 || hits[0].GetId() != "arch" || hits[0].GetSummary() != "no qwen9" || hits[0].GetHint() != "build for qwen9" || hits[0].GetFix()["a"] != "b" {
		t.Fatalf("hits %v", hits)
	}
	if hits[1].GetId() != "oom" || hits[1].GetLine() != "CUDA out of memory" {
		t.Fatalf("hits %v", hits)
	}
	if len(m.Scan([]string{"x"}, []string{"fine"})) != 0 {
		t.Fatal("no hits expected")
	}
	if _, err := New([]*v1.TriageSpec{{Id: "bad", Rules: []*v1.TriageRule{{Id: "r", Match: "("}}}}); err == nil {
		t.Fatal("bad regex should fail")
	}
}
