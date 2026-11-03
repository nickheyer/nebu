package bench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

// A latency distribution in milliseconds
type Summary struct {
	Count int     `json:"count"`
	Mean  float64 `json:"mean_ms"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Max   float64 `json:"max_ms"`
}

// Latency, throughput and failures of one replay
type Report struct {
	// The scheduler configuration the run measured
	Name        string  `json:"name"`
	Trace       string  `json:"trace,omitempty"`
	Speed       float64 `json:"speed"`
	Concurrency int     `json:"concurrency"`
	Requests    int     `json:"requests"`
	Succeeded   int     `json:"succeeded"`
	// Successful requests answered as a stream, the TTFT and TPOT population
	Streamed int `json:"streamed"`
	// Requests answered 429, shed or rate limited
	Shed int `json:"shed"`
	// Requests answered 503
	Unavailable int `json:"unavailable"`
	// Requests failing any other way, by status or by transport and stream errors
	Errors   int         `json:"errors"`
	Statuses map[int]int `json:"statuses,omitempty"`
	// Distinct failure messages in order of first sight, at most ten
	Failures []string `json:"failures,omitempty"`
	// Wall time from the first arrival to the last answer
	DurationMS            float64 `json:"duration_ms"`
	PromptTokens          int64   `json:"prompt_tokens"`
	OutputTokens          int64   `json:"output_tokens"`
	RequestsPerSecond     float64 `json:"requests_per_second"`
	OutputTokensPerSecond float64 `json:"output_tokens_per_second"`
	TTFT                  Summary `json:"ttft"`
	TPOT                  Summary `json:"tpot"`
	E2E                   Summary `json:"e2e"`
	// How late requests left against their trace arrival time
	Lag Summary `json:"dispatch_lag"`
	// Requests by the endpoint the gateway reported serving them
	Endpoints map[string]int `json:"endpoints,omitempty"`
}

// The linearly interpolated percentile p of ascending values, zero for none
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := p / 100 * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(rank-float64(lo))
}

// Count, mean, p50, p95, p99 and max of values
func summarize(values []float64) Summary {
	if len(values) == 0 {
		return Summary{}
	}
	s := slices.Clone(values)
	slices.Sort(s)
	total := 0.0
	for _, v := range s {
		total += v
	}
	return Summary{Count: len(s), Mean: total / float64(len(s)), P50: percentile(s, 50), P95: percentile(s, 95), P99: percentile(s, 99), Max: s[len(s)-1]}
}

// Coefficient of variation of requests per endpoint, zero for fewer than two endpoints
func (r *Report) EndpointCV() float64 {
	if len(r.Endpoints) < 2 {
		return 0
	}
	mean, sq := 0.0, 0.0
	for _, n := range r.Endpoints {
		mean += float64(n)
	}
	mean /= float64(len(r.Endpoints))
	for _, n := range r.Endpoints {
		sq += (float64(n) - mean) * (float64(n) - mean)
	}
	return math.Sqrt(sq/float64(len(r.Endpoints))) / mean
}

// Writes the report as indented JSON
func (r *Report) WriteJSON(w io.Writer) error {
	return writeJSON(w, r)
}

// Writes the report as Markdown tables of counts, latency percentiles and endpoint spread
func (r *Report) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", r.Name)
	b.WriteString("| requests | succeeded | streamed | shed 429 | unavailable 503 | errors | req/s | output tok/s | duration s |\n")
	b.WriteString("|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	fmt.Fprintf(&b, "| %d | %d | %d | %d | %d | %d | %s | %s | %s |\n\n", r.Requests, r.Succeeded, r.Streamed, r.Shed, r.Unavailable, r.Errors,
		num(r.RequestsPerSecond), num(r.OutputTokensPerSecond), num(r.DurationMS/1000))
	b.WriteString("| ms | count | p50 | p95 | p99 | mean | max |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|\n")
	for _, row := range []struct {
		name string
		s    Summary
	}{{"TTFT", r.TTFT}, {"TPOT", r.TPOT}, {"E2E", r.E2E}, {"dispatch lag", r.Lag}} {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s |\n", row.name, row.s.Count, num(row.s.P50), num(row.s.P95), num(row.s.P99), num(row.s.Mean), num(row.s.Max))
	}
	if len(r.Endpoints) > 0 {
		b.WriteString("\n| endpoint | requests |\n|---|---:|\n")
		for _, ep := range slices.Sorted(maps.Keys(r.Endpoints)) {
			fmt.Fprintf(&b, "| %s | %d |\n", ep, r.Endpoints[ep])
		}
	}
	if len(r.Statuses) > 0 {
		b.WriteString("\n| status | requests |\n|---:|---:|\n")
		for _, st := range slices.Sorted(maps.Keys(r.Statuses)) {
			fmt.Fprintf(&b, "| %d | %d |\n", st, r.Statuses[st])
		}
	}
	for _, f := range r.Failures {
		fmt.Fprintf(&b, "\n- %s", f)
	}
	if len(r.Failures) > 0 {
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Several runs of one trace side by side, one per scheduler configuration
type Comparison struct {
	Runs []*Report `json:"runs"`
}

// Puts runs side by side in the order given
func Compare(runs ...*Report) (*Comparison, error) {
	if len(runs) == 0 {
		return nil, errors.New("compare needs at least one run")
	}
	for i, r := range runs {
		if r == nil {
			return nil, fmt.Errorf("run %d is missing", i)
		}
	}
	return &Comparison{Runs: slices.Clone(runs)}, nil
}

// Writes every run as indented JSON
func (c *Comparison) WriteJSON(w io.Writer) error {
	return writeJSON(w, c)
}

// Writes one Markdown table with a row per run of counts, percentiles, throughput and endpoint spread
func (c *Comparison) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	b.WriteString("| config | requests | ok | shed 429 | unavail 503 | errors | TTFT p50 | TTFT p95 | TTFT p99 | TPOT p50 | TPOT p95 | TPOT p99 | E2E p50 | E2E p95 | E2E p99 | req/s | out tok/s | endpoint cv |\n")
	b.WriteString("|---|" + strings.Repeat("---:|", 17) + "\n")
	for _, r := range c.Runs {
		cells := []string{r.Name, strconv.Itoa(r.Requests), strconv.Itoa(r.Succeeded), strconv.Itoa(r.Shed), strconv.Itoa(r.Unavailable), strconv.Itoa(r.Errors),
			num(r.TTFT.P50), num(r.TTFT.P95), num(r.TTFT.P99), num(r.TPOT.P50), num(r.TPOT.P95), num(r.TPOT.P99),
			num(r.E2E.P50), num(r.E2E.P95), num(r.E2E.P99), num(r.RequestsPerSecond), num(r.OutputTokensPerSecond), num(r.EndpointCV())}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	b.WriteString("\nLatencies in milliseconds, TTFT and TPOT over streamed successes, E2E over every success.\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// Reads runs from JSON holding one report, a list of reports, or a comparison
func ReadReports(r io.Reader) ([]*Report, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '[' {
		var list []*Report
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, err
	}
	if _, ok := probe["runs"]; ok {
		var c Comparison
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, err
		}
		return c.Runs, nil
	}
	if _, ok := probe["name"]; !ok {
		return nil, errors.New("JSON is neither a report, a list of reports, nor a comparison")
	}
	var one Report
	if err := json.Unmarshal(data, &one); err != nil {
		return nil, err
	}
	return []*Report{&one}, nil
}

// Indented JSON with HTML left unescaped
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// A number with up to two decimals and no trailing zeros
func num(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}
