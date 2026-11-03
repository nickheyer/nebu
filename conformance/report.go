// Package conformance checks a gateway and scheduler implementation against the nebu routing protocol over the network
package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// A named behavior of the routing protocol an implementation passes or fails as a whole
type Feature string

const (
	FeatureEndpointServed              Feature = "EndpointServed"
	FeatureEndpointConfinement         Feature = "EndpointConfinement"
	FeatureDataParallelPortConfinement Feature = "DataParallelPortConfinement"
	FeatureWeightedSplit               Feature = "WeightedSplit"
	FeatureFailOpen                    Feature = "FailOpen"
	FeatureInvalidPoolRef              Feature = "InvalidPoolRef"
	FeatureMissingSchedulerRef         Feature = "MissingSchedulerRef"
	FeatureAppProtocolDefaults         Feature = "AppProtocolDefaults"
)

// What each feature requires of an implementation, in report order
var Features = []struct {
	Name        Feature
	Description string
}{
	{FeatureEndpointServed, "Every response names the endpoint that actually served it in X-Gateway-Destination-Endpoint-Served"},
	{FeatureEndpointConfinement, "X-Gateway-Destination-Endpoint-Subset confines every pick to the named endpoints and answers 503 when none is eligible"},
	{FeatureDataParallelPortConfinement, "Each target port of one host is its own endpoint and a port outside target_ports never serves"},
	{FeatureWeightedSplit, "A route splits traffic over pool targets in proportion to their weights"},
	{FeatureFailOpen, "With the scheduler unreachable a FAIL_OPEN pool still serves and a FAIL_CLOSE pool answers 503"},
	{FeatureInvalidPoolRef, "A route whose target pool does not exist reports ResolvedRefs False with reason InvalidPoolRef and answers 503"},
	{FeatureMissingSchedulerRef, "A pool naming an unknown scheduler config reports UnknownSchedulerConfig and answers 503 under every failure mode"},
	{FeatureAppProtocolDefaults, "A pool created without app_protocol reads back APP_PROTOCOL_HTTP and reaches its endpoints over HTTP/1.1"},
}

// Whether a feature passed every check
type Outcome string

const (
	OutcomePassed Outcome = "passed"
	OutcomeFailed Outcome = "failed"
)

// The report schema version, bumped when a field changes meaning
const SchemaVersion = "conformance.nebu/v1"

// One assertion inside a feature with the evidence it was judged on
type Check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// A feature's outcome, every check it made, and how long it ran
type FeatureResult struct {
	Name        Feature `json:"name"`
	Description string  `json:"description"`
	Outcome     Outcome `json:"outcome"`
	DurationMS  int64   `json:"durationMs"`
	Checks      []Check `json:"checks"`
}

// The software under test as it names itself through the gateway status RPC
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Feature counts by outcome
type Summary struct {
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

// One run of the suite against one implementation
type Report struct {
	Schema         string          `json:"schema"`
	Implementation Implementation  `json:"implementation"`
	Date           time.Time       `json:"date"`
	Summary        Summary         `json:"summary"`
	Features       []FeatureResult `json:"features"`
}

// Orders results by the feature list and counts outcomes, refusing a report missing any feature
func NewReport(impl Implementation, date time.Time, results []FeatureResult) (*Report, error) {
	known := make(map[Feature]bool, len(Features))
	for _, f := range Features {
		known[f.Name] = true
	}
	byName := make(map[Feature]FeatureResult, len(results))
	for _, r := range results {
		if !known[r.Name] {
			return nil, fmt.Errorf("feature %s is not part of the suite", r.Name)
		}
		if _, dup := byName[r.Name]; dup {
			return nil, fmt.Errorf("feature %s reported twice", r.Name)
		}
		byName[r.Name] = r
	}
	if impl.Name == "" || impl.Version == "" {
		return nil, fmt.Errorf("implementation name %q and version %q must both be set", impl.Name, impl.Version)
	}
	rep := &Report{Schema: SchemaVersion, Implementation: impl, Date: date.UTC().Truncate(time.Second)}
	var missing []string
	for _, f := range Features {
		r, ok := byName[f.Name]
		if !ok {
			missing = append(missing, string(f.Name))
			continue
		}
		switch r.Outcome {
		case OutcomePassed:
			rep.Summary.Passed++
		case OutcomeFailed:
			rep.Summary.Failed++
		default:
			return nil, fmt.Errorf("feature %s has outcome %q", r.Name, r.Outcome)
		}
		rep.Features = append(rep.Features, r)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("features %s did not run", strings.Join(missing, ", "))
	}
	return rep, nil
}

// The file stem of a report, implementation dash version with characters unsafe in paths replaced
func (r *Report) Stem() string {
	clean := func(s string) string {
		return strings.Map(func(c rune) rune {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '+':
				return c
			}
			return '-'
		}, strings.TrimSpace(s))
	}
	return clean(r.Implementation.Name) + "-" + clean(r.Implementation.Version)
}

// The report as YAML
func (r *Report) YAML() ([]byte, error) { return yaml.Marshal(r) }

// The report as a feature table followed by one check table per feature
func (r *Report) Markdown() string {
	var b strings.Builder
	cell := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ") }
	fmt.Fprintf(&b, "# %s %s conformance\n\n", r.Implementation.Name, r.Implementation.Version)
	fmt.Fprintf(&b, "Schema %s, run %s, %d passed, %d failed\n\n", r.Schema, r.Date.Format(time.RFC3339), r.Summary.Passed, r.Summary.Failed)
	b.WriteString("| Feature | Outcome | Checks | Duration |\n|---|---|---|---|\n")
	for _, f := range r.Features {
		passed := 0
		for _, c := range f.Checks {
			if c.Passed {
				passed++
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %d/%d | %dms |\n", f.Name, f.Outcome, passed, len(f.Checks), f.DurationMS)
	}
	for _, f := range r.Features {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n\n| Check | Passed | Detail |\n|---|---|---|\n", f.Name, cell(f.Description))
		for _, c := range f.Checks {
			fmt.Fprintf(&b, "| %s | %t | %s |\n", cell(c.Name), c.Passed, cell(c.Detail))
		}
	}
	return b.String()
}

// Writes the report as stem.yaml and stem.md under dir, returning both paths
func (r *Report) Write(dir string) (string, string, error) {
	data, err := r.YAML()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	yml := filepath.Join(dir, r.Stem()+".yaml")
	md := filepath.Join(dir, r.Stem()+".md")
	if err := os.WriteFile(yml, data, 0o644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(md, []byte(r.Markdown()), 0o644); err != nil {
		return "", "", err
	}
	return yml, md, nil
}
