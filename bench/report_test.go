package bench

import (
	"bytes"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestPercentile(t *testing.T) {
	hundred := make([]float64, 100)
	for i := range hundred {
		hundred[i] = float64(i + 1)
	}
	cases := []struct {
		name   string
		values []float64
		p      float64
		want   float64
	}{
		{"none", nil, 50, 0},
		{"one", []float64{7}, 99, 7},
		{"two midpoint", []float64{10, 20}, 50, 15},
		{"two p95", []float64{10, 20}, 95, 19.5},
		{"p0 is min", hundred, 0, 1},
		{"p100 is max", hundred, 100, 100},
		{"p50 of 1 to 100", hundred, 50, 50.5},
		{"p95 of 1 to 100", hundred, 95, 95.05},
		{"p99 of 1 to 100", hundred, 99, 99.01},
		{"exact rank", []float64{1, 2, 3, 4, 5}, 75, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := percentile(tc.values, tc.p); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	in := []float64{5, 1, 4, 2, 3}
	got := summarize(in)
	want := Summary{Count: 5, Mean: 3, P50: 3, P95: 4.8, P99: 4.96, Max: 5}
	if math.Abs(got.P95-want.P95) > 1e-9 || math.Abs(got.P99-want.P99) > 1e-9 {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	got.P95, got.P99 = want.P95, want.P99
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if !slices.Equal(in, []float64{5, 1, 4, 2, 3}) {
		t.Fatal("summarize sorted its input in place")
	}
	if summarize(nil) != (Summary{}) {
		t.Fatal("no values gave a non zero summary")
	}
}

func TestEndpointCV(t *testing.T) {
	cases := []struct {
		eps  map[string]int
		want float64
	}{
		{nil, 0},
		{map[string]int{"a": 9}, 0},
		{map[string]int{"a": 5, "b": 5}, 0},
		{map[string]int{"a": 2, "b": 6}, 0.5},
	}
	for _, tc := range cases {
		r := &Report{Endpoints: tc.eps}
		if got := r.EndpointCV(); math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("%v gave %v, want %v", tc.eps, got, tc.want)
		}
	}
}

// A report with every field set
func sampleReport(name string, scale float64) *Report {
	return &Report{
		Name: name, Trace: "t.jsonl", Speed: 2, Concurrency: 64, Requests: 10, Succeeded: 7, Streamed: 6,
		Shed: 1, Unavailable: 1, Errors: 1, Statuses: map[int]int{429: 1, 503: 1, 500: 1},
		Failures:   []string{"status 500 boom"},
		DurationMS: 1500, PromptTokens: 700, OutputTokens: 112, RequestsPerSecond: 4.67, OutputTokensPerSecond: 74.67,
		TTFT:      Summary{Count: 6, Mean: 20 * scale, P50: 18 * scale, P95: 30 * scale, P99: 31 * scale, Max: 32 * scale},
		TPOT:      Summary{Count: 6, Mean: 10 * scale, P50: 9 * scale, P95: 12 * scale, P99: 13 * scale, Max: 14 * scale},
		E2E:       Summary{Count: 7, Mean: 200 * scale, P50: 190 * scale, P95: 250 * scale, P99: 260 * scale, Max: 270 * scale},
		Lag:       Summary{Count: 10, Mean: 0.1, P50: 0.1, P95: 0.2, P99: 0.3, Max: 0.4},
		Endpoints: map[string]int{"127.0.0.1:1": 4, "127.0.0.1:2": 3},
	}
}

func TestReportJSONRoundTrip(t *testing.T) {
	want := sampleReport("default", 1)
	var buf bytes.Buffer
	if err := want.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := ReadReports(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("got %+v", got)
	}
}

func TestReportMarkdown(t *testing.T) {
	var buf bytes.Buffer
	if err := sampleReport("default", 1).WriteMarkdown(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"## default",
		"| 10 | 7 | 6 | 1 | 1 | 1 | 4.67 | 74.67 | 1.5 |",
		"| TTFT | 6 | 18 | 30 | 31 | 20 | 32 |",
		"| TPOT | 6 | 9 | 12 | 13 | 10 | 14 |",
		"| E2E | 7 | 190 | 250 | 260 | 200 | 270 |",
		"| 127.0.0.1:1 | 4 |",
		"| 429 | 1 |",
		"- status 500 boom",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("markdown lacks %q\n%s", want, out)
		}
	}
}

func TestCompare(t *testing.T) {
	a, b := sampleReport("default", 1), sampleReport("default-nometrics", 2)
	b.Endpoints = map[string]int{"127.0.0.1:1": 7}
	c, err := Compare(a, b)
	if err != nil {
		t.Fatal(err)
	}
	var md bytes.Buffer
	if err := c.WriteMarkdown(&md); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(md.String(), "\n")
	if !strings.HasPrefix(lines[0], "| config | requests | ok | shed 429 | unavail 503 | errors | TTFT p50 |") {
		t.Fatalf("header %q", lines[0])
	}
	if strings.Count(lines[1], "---") != 18 {
		t.Fatalf("separator %q", lines[1])
	}
	wantRows := []string{
		"| default | 10 | 7 | 1 | 1 | 1 | 18 | 30 | 31 | 9 | 12 | 13 | 190 | 250 | 260 | 4.67 | 74.67 | 0.14 |",
		"| default-nometrics | 10 | 7 | 1 | 1 | 1 | 36 | 60 | 62 | 18 | 24 | 26 | 380 | 500 | 520 | 4.67 | 74.67 | 0 |",
	}
	if lines[2] != wantRows[0] || lines[3] != wantRows[1] {
		t.Fatalf("rows\n%s\n%s", lines[2], lines[3])
	}
	for _, form := range []string{"comparison", "list"} {
		var js bytes.Buffer
		if form == "comparison" {
			c.WriteJSON(&js)
		} else {
			writeJSON(&js, []*Report{a, b})
		}
		got, err := ReadReports(&js)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, []*Report{a, b}) {
			t.Fatalf("%s form read back %+v", form, got)
		}
	}
}

func TestCompareRefuses(t *testing.T) {
	if _, err := Compare(); err == nil {
		t.Fatal("no runs compared")
	}
	if _, err := Compare(sampleReport("a", 1), nil); err == nil {
		t.Fatal("a missing run compared")
	}
}

func TestReadReportsRefuses(t *testing.T) {
	for _, in := range []string{"", "nope", `{"other":1}`, `[1]`} {
		if _, err := ReadReports(strings.NewReader(in)); err == nil {
			t.Fatalf("%q read as reports", in)
		}
	}
}
