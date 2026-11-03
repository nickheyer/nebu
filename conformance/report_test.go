package conformance

import (
	"os"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

// Every feature passed except the named ones, which failed
func results(failed ...Feature) []FeatureResult {
	out := make([]FeatureResult, 0, len(Features))
	for _, f := range Features {
		r := FeatureResult{Name: f.Name, Description: f.Description, Outcome: OutcomePassed, Checks: []Check{{Name: "ok", Passed: true, Detail: "fine"}}}
		for _, x := range failed {
			if x == f.Name {
				r.Outcome = OutcomeFailed
				r.Checks = append(r.Checks, Check{Name: "bad | pipe", Detail: "line\nbreak"})
			}
		}
		out = append(out, r)
	}
	return out
}

func TestNewReport(t *testing.T) {
	nebu := Implementation{Name: "nebu", Version: "v1.2.3"}
	all := results(FeatureWeightedSplit)
	reversed := make([]FeatureResult, len(all))
	for i, r := range all {
		reversed[len(all)-1-i] = r
	}
	badOutcome := results()
	badOutcome[0].Outcome = "skipped"
	cases := []struct {
		name    string
		impl    Implementation
		results []FeatureResult
		wantErr string
		passed  int
		failed  int
	}{
		{"every feature in suite order", nebu, reversed, "", len(Features) - 1, 1},
		{"missing feature", nebu, all[1:], "did not run", 0, 0},
		{"duplicate feature", nebu, append(results(), all[0]), "reported twice", 0, 0},
		{"unknown feature", nebu, append(results(), FeatureResult{Name: "Extra", Outcome: OutcomePassed}), "not part of the suite", 0, 0},
		{"outcome outside the schema", nebu, badOutcome, "has outcome", 0, 0},
		{"unnamed implementation", Implementation{Version: "v1"}, all, "must both be set", 0, 0},
		{"unversioned implementation", Implementation{Name: "nebu"}, all, "must both be set", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep, err := NewReport(c.impl, time.Date(2026, 11, 3, 1, 2, 3, 4, time.FixedZone("x", 3600)), c.results)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if rep.Summary.Passed != c.passed || rep.Summary.Failed != c.failed || rep.Schema != SchemaVersion {
				t.Fatalf("summary %+v schema %s", rep.Summary, rep.Schema)
			}
			for i, f := range Features {
				if rep.Features[i].Name != f.Name {
					t.Fatalf("feature %d is %s, want %s", i, rep.Features[i].Name, f.Name)
				}
			}
			if want := time.Date(2026, 11, 3, 0, 2, 3, 0, time.UTC); !rep.Date.Equal(want) || rep.Date.Location() != time.UTC {
				t.Fatalf("date %s, want %s", rep.Date, want)
			}
		})
	}
}

func TestStem(t *testing.T) {
	cases := []struct {
		impl Implementation
		want string
	}{
		{Implementation{"nebu", "v1.2.3"}, "nebu-v1.2.3"},
		{Implementation{"nebu", "v0.0.0-20261103-abc+dirty"}, "nebu-v0.0.0-20261103-abc+dirty"},
		{Implementation{"my gateway", "1.0/rc 1"}, "my-gateway-1.0-rc-1"},
		{Implementation{" spaced ", "../up"}, "spaced-..-up"},
	}
	for _, c := range cases {
		if got := (&Report{Implementation: c.impl}).Stem(); got != c.want {
			t.Fatalf("stem of %+v is %q, want %q", c.impl, got, c.want)
		}
	}
}

func TestWriteYAMLAndMarkdown(t *testing.T) {
	rep, err := NewReport(Implementation{Name: "nebu", Version: "v9"}, time.Unix(0, 0), results(FeatureFailOpen))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	yml, md, err := rep.Write(dir + "/reports")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(yml, "/reports/nebu-v9.yaml") || !strings.HasSuffix(md, "/reports/nebu-v9.md") {
		t.Fatalf("paths %s %s", yml, md)
	}
	raw, err := os.ReadFile(yml)
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := yaml.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Implementation != rep.Implementation || back.Summary != rep.Summary || len(back.Features) != len(Features) || !back.Date.Equal(rep.Date) {
		t.Fatalf("YAML read back as %+v", back)
	}
	for _, want := range []string{"schema: conformance.nebu/v1", "outcome: failed", "name: FailOpen"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("YAML lacks %q\n%s", want, raw)
		}
	}
	text, err := os.ReadFile(md)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# nebu v9 conformance",
		"7 passed, 1 failed",
		"| Feature | Outcome | Checks | Duration |",
		"| FailOpen | failed | 1/2 | 0ms |",
		"| EndpointServed | passed | 1/1 | 0ms |",
		"## WeightedSplit",
		`| bad \| pipe | false | line break |`,
	} {
		if !strings.Contains(string(text), want) {
			t.Fatalf("Markdown lacks %q\n%s", want, text)
		}
	}
}
