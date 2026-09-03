package triage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/spec"
	specfs "github.com/nickheyer/nebu/spec"
)

// Captured runtime logs matched against the shipped triage sets
func TestFixtureLogs(t *testing.T) {
	c, err := spec.Load(specfs.FS())
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(c.Triage)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		set  string
		want string
	}{
		"llamacpp-oom.log":          {"llamacpp", "device-oom"},
		"llamacpp-unknown-arch.log": {"llamacpp", "unknown-arch"},
		"llamacpp-bad-flag.log":     {"llamacpp", "bad-flag"},
		"llamacpp-port.log":         {"llamacpp", "port-in-use"},
		"llamacpp-cache-flash.log":  {"llamacpp", "cache-quant-needs-flash"},
		"llamacpp-healthy.log":      {"llamacpp", ""},
		"vllm-oom.log":              {"vllm", "device-oom"},
		"vllm-kv.log":               {"vllm", "kv-cache-too-small"},
	}
	for name, tc := range cases {
		data, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "logs", name))
		if err != nil {
			t.Fatal(err)
		}
		hits := m.Scan([]string{tc.set}, strings.Split(strings.TrimSpace(string(data)), "\n"))
		ids := map[string]bool{}
		for _, h := range hits {
			ids[h.GetId()] = true
			if h.GetSummary() == "" || h.GetHint() == "" || h.GetLine() == "" {
				t.Errorf("%s: hit %s missing summary, hint, or line", name, h.GetId())
			}
		}
		if tc.want == "" {
			if len(hits) > 0 {
				t.Errorf("%s: healthy log tripped %v", name, ids)
			}
			continue
		}
		if !ids[tc.want] {
			t.Errorf("%s: want rule %s, got %v", name, tc.want, ids)
		}
	}
}

func TestExpandsGroups(t *testing.T) {
	c, err := spec.Load(specfs.FS())
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(c.Triage)
	if err != nil {
		t.Fatal(err)
	}
	hits := m.Scan([]string{"llamacpp"}, []string{"llama_model_load: error loading model: unknown model architecture: 'gemma4'"})
	found := false
	for _, h := range hits {
		if h.GetId() == "unknown-arch" {
			found = true
			if !strings.Contains(h.GetSummary(), "gemma4") || !strings.Contains(h.GetHint(), "gemma4") {
				t.Errorf("groups not expanded: %q %q", h.GetSummary(), h.GetHint())
			}
		}
	}
	if !found {
		t.Fatal("unknown-arch did not match")
	}
}
