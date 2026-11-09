package metrics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/trace"

	"github.com/nickheyer/nebu/pkg/flowcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

func newRegistry(t *testing.T) (*Registry, *prometheus.Registry) {
	t.Helper()
	preg := prometheus.NewRegistry()
	r, err := New(preg)
	if err != nil {
		t.Fatal(err)
	}
	return r, preg
}

func gather(t *testing.T, preg *prometheus.Registry) map[string]*dto.MetricFamily {
	t.Helper()
	mfs, err := preg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*dto.MetricFamily{}
	for _, mf := range mfs {
		out[mf.GetName()] = mf
	}
	return out
}

func labelString(m *dto.Metric) string {
	var parts []string
	for _, l := range m.GetLabel() {
		parts = append(parts, l.GetName()+"="+l.GetValue())
	}
	return strings.Join(parts, ",")
}

// Every series of a gauge or counter family keyed by its sorted label pairs
func values(t *testing.T, preg *prometheus.Registry, name string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	mf, ok := gather(t, preg)[name]
	if !ok {
		return out
	}
	for _, m := range mf.GetMetric() {
		switch {
		case m.Gauge != nil:
			out[labelString(m)] = m.GetGauge().GetValue()
		case m.Counter != nil:
			out[labelString(m)] = m.GetCounter().GetValue()
		case m.Histogram != nil:
			out[labelString(m)] = float64(m.GetHistogram().GetSampleCount())
		}
	}
	return out
}

func TestFamiliesAndLabels(t *testing.T) {
	r, preg := newRegistry(t)
	ctx := context.Background()
	r.ObserveRequest(ctx, Request{Model: "m", Pool: "p", Endpoint: "e", Priority: 1, FairnessID: "f", Status: 200, Duration: time.Second, TTFT: 100 * time.Millisecond, TPOT: 20 * time.Millisecond, PromptTokens: 10, OutputTokens: 5, Admitted: true, QueueWait: time.Millisecond})
	r.OnAdmit("p", 1, time.Millisecond, flowcontrol.OutcomeShed)
	r.ObservePick("p", "default", time.Microsecond)
	r.FilterDropped("p", "default", "kv", 2)
	r.ObserveScore("p", "default", "queue", 0.5)
	r.SchedulerFallback("p", "remote_unavailable")
	r.ObserveObjectives("p", framework.Objective{Priority: 1, TTFT: time.Second}, &framework.Response{StartedAt: time.Unix(0, 0), FirstTokenAt: time.Unix(0, int64(time.Millisecond))})
	r.ObservePrefixHitRatio("p", 0.75)
	r.SetPrefixCacheEntries(42)
	r.queueDepth.WithLabelValues("p", "1").Set(3)
	for _, g := range []*prometheus.GaugeVec{r.endpointQueued, r.endpointRunning, r.endpointKV, r.endpointAssumed, r.endpointReady} {
		g.WithLabelValues("p", "e").Set(1)
	}
	r.OnScrape("p", "e", time.Millisecond, errors.New("refused"))
	r.poolReady.WithLabelValues("p").Set(1)
	r.poolKV.WithLabelValues("p").Set(0.5)
	r.poolQueued.WithLabelValues("p").Set(2)

	perRequest := []string{"model", "pool", "endpoint", "priority", "fairness_id"}
	want := map[string]struct {
		typ    dto.MetricType
		labels []string
	}{
		"nebu_request_total":                    {dto.MetricType_COUNTER, []string{"model", "pool", "endpoint", "priority", "fairness_id", "status"}},
		"nebu_request_duration_seconds":         {dto.MetricType_HISTOGRAM, perRequest},
		"nebu_request_ttft_seconds":             {dto.MetricType_HISTOGRAM, perRequest},
		"nebu_request_tpot_seconds":             {dto.MetricType_HISTOGRAM, perRequest},
		"nebu_request_queue_wait_seconds":       {dto.MetricType_HISTOGRAM, perRequest},
		"nebu_request_prompt_tokens":            {dto.MetricType_HISTOGRAM, perRequest},
		"nebu_request_output_tokens":            {dto.MetricType_HISTOGRAM, perRequest},
		"nebu_request_shed_total":               {dto.MetricType_COUNTER, []string{"pool", "priority", "reason"}},
		"nebu_flow_queue_depth":                 {dto.MetricType_GAUGE, []string{"pool", "priority"}},
		"nebu_scheduler_pick_duration_seconds":  {dto.MetricType_HISTOGRAM, []string{"pool", "profile"}},
		"nebu_scheduler_filter_dropped_total":   {dto.MetricType_COUNTER, []string{"pool", "profile", "filter"}},
		"nebu_scheduler_score":                  {dto.MetricType_HISTOGRAM, []string{"pool", "profile", "scorer"}},
		"nebu_scheduler_fallback_total":         {dto.MetricType_COUNTER, []string{"pool", "reason"}},
		"nebu_endpoint_queued_requests":         {dto.MetricType_GAUGE, []string{"pool", "endpoint"}},
		"nebu_endpoint_running_requests":        {dto.MetricType_GAUGE, []string{"pool", "endpoint"}},
		"nebu_endpoint_kv_cache_utilization":    {dto.MetricType_GAUGE, []string{"pool", "endpoint"}},
		"nebu_endpoint_assumed_requests":        {dto.MetricType_GAUGE, []string{"pool", "endpoint"}},
		"nebu_endpoint_ready":                   {dto.MetricType_GAUGE, []string{"pool", "endpoint"}},
		"nebu_endpoint_scrape_duration_seconds": {dto.MetricType_HISTOGRAM, []string{"pool"}},
		"nebu_endpoint_scrape_errors_total":     {dto.MetricType_COUNTER, []string{"pool", "endpoint"}},
		"nebu_pool_ready_endpoints":             {dto.MetricType_GAUGE, []string{"pool"}},
		"nebu_pool_kv_cache_utilization":        {dto.MetricType_GAUGE, []string{"pool"}},
		"nebu_pool_queued_requests":             {dto.MetricType_GAUGE, []string{"pool"}},
		"nebu_objective_attained_total":         {dto.MetricType_COUNTER, []string{"pool", "priority", "objective", "attained"}},
		"nebu_prefix_cache_hit_ratio":           {dto.MetricType_HISTOGRAM, []string{"pool"}},
		"nebu_prefix_cache_entries":             {dto.MetricType_GAUGE, nil},
		"nebu_fairness_prompt_tokens_total":     {dto.MetricType_COUNTER, []string{"fairness_id"}},
		"nebu_fairness_output_tokens_total":     {dto.MetricType_COUNTER, []string{"fairness_id"}},
	}

	got := gather(t, preg)
	var names []string
	for name := range got {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(got) != len(want) {
		t.Fatalf("gathered %d families, want %d: %v", len(got), len(want), names)
	}
	for name, w := range want {
		mf, ok := got[name]
		if !ok {
			t.Fatalf("family %s missing from %v", name, names)
		}
		if mf.GetType() != w.typ {
			t.Fatalf("%s type %v, want %v", name, mf.GetType(), w.typ)
		}
		var labels []string
		for _, l := range mf.GetMetric()[0].GetLabel() {
			labels = append(labels, l.GetName())
		}
		wantLabels := slices.Clone(w.labels)
		sort.Strings(labels)
		sort.Strings(wantLabels)
		if !slices.Equal(labels, wantLabels) {
			t.Fatalf("%s labels %v, want %v", name, labels, wantLabels)
		}
	}
}

func TestRegistriesAreIsolated(t *testing.T) {
	preg := prometheus.NewRegistry()
	if _, err := New(preg); err != nil {
		t.Fatal(err)
	}
	if _, err := New(preg); err == nil {
		t.Fatal("second registration on one registry succeeded")
	}
	partial := prometheus.NewRegistry()
	partial.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: "nebu_prefix_cache_entries", Help: "taken"}))
	if _, err := New(partial); err == nil {
		t.Fatal("registration over a taken name succeeded")
	}
	if mfs, _ := partial.Gather(); len(mfs) != 1 {
		t.Fatalf("failed registration left %d families behind", len(mfs))
	}
	a, pa := newRegistry(t)
	_, pb := newRegistry(t)
	a.SetPrefixCacheEntries(7)
	if v := values(t, pa, "nebu_prefix_cache_entries")[""]; v != 7 {
		t.Fatalf("registry a holds %v", v)
	}
	if v := values(t, pb, "nebu_prefix_cache_entries")[""]; v != 0 {
		t.Fatalf("registry b saw a's write: %v", v)
	}
}

func TestObserveRequestSkipsMissingLatencies(t *testing.T) {
	r, preg := newRegistry(t)
	r.ObserveRequest(context.Background(), Request{Model: "m", Pool: "p", Status: 503, Duration: time.Millisecond})
	if got := values(t, preg, "nebu_request_total"); got["endpoint=,fairness_id=,model=m,pool=p,priority=0,status=503"] != 1 {
		t.Fatalf("request_total %v", got)
	}
	for _, name := range []string{"nebu_request_ttft_seconds", "nebu_request_tpot_seconds"} {
		if got := values(t, preg, name); len(got) != 0 {
			t.Fatalf("%s observed without a token: %v", name, got)
		}
	}
	if got := values(t, preg, "nebu_request_output_tokens")["endpoint=,fairness_id=,model=m,pool=p,priority=0"]; got != 1 {
		t.Fatalf("output tokens count %v", got)
	}
	if got := values(t, preg, "nebu_request_queue_wait_seconds"); len(got) != 0 {
		t.Fatalf("queue wait observed for a request flow control never admitted: %v", got)
	}
}

// Queue wait is observed per admitted request under the request's own labels
func TestObserveRequestQueueWait(t *testing.T) {
	r, preg := newRegistry(t)
	r.ObserveRequest(context.Background(), Request{Model: "m", Pool: "p", Endpoint: "e", Priority: 3, FairnessID: "f", Status: 200, Admitted: true, QueueWait: 250 * time.Millisecond})
	mf := gather(t, preg)["nebu_request_queue_wait_seconds"]
	if mf == nil || len(mf.GetMetric()) != 1 {
		t.Fatalf("queue wait family %v", mf)
	}
	m := mf.GetMetric()[0]
	if labelString(m) != "endpoint=e,fairness_id=f,model=m,pool=p,priority=3" || m.GetHistogram().GetSampleCount() != 1 || m.GetHistogram().GetSampleSum() != 0.25 {
		t.Fatalf("queue wait series %s %v", labelString(m), m.GetHistogram())
	}
}

func TestOnAdmit(t *testing.T) {
	tests := []struct {
		outcome  string
		wantShed bool
	}{
		{flowcontrol.OutcomeDispatched, false},
		{flowcontrol.OutcomeShed, true},
		{flowcontrol.OutcomeQueueFull, true},
		{flowcontrol.OutcomeTimeout, true},
		{flowcontrol.OutcomeCanceled, true},
		{flowcontrol.OutcomeClosed, true},
	}
	for _, tc := range tests {
		t.Run(tc.outcome, func(t *testing.T) {
			r, preg := newRegistry(t)
			r.OnAdmit("p", 2, 30*time.Millisecond, tc.outcome)
			if got := values(t, preg, "nebu_request_queue_wait_seconds"); len(got) != 0 {
				t.Fatalf("an admission observed queue wait outside the request: %v", got)
			}
			shed := values(t, preg, "nebu_request_shed_total")
			if tc.wantShed != (shed["pool=p,priority=2,reason="+tc.outcome] == 1) || (!tc.wantShed && len(shed) != 0) {
				t.Fatalf("shed %v", shed)
			}
		})
	}
}

func TestObserveObjectives(t *testing.T) {
	start := time.Unix(1000, 0)
	at := func(d time.Duration) time.Time { return start.Add(d) }
	tests := []struct {
		name string
		obj  framework.Objective
		resp framework.Response
		want map[string]float64
	}{
		{
			name: "both met",
			obj:  framework.Objective{Priority: 1, TTFT: time.Second, TPOT: 50 * time.Millisecond},
			resp: framework.Response{StartedAt: start, FirstTokenAt: at(500 * time.Millisecond), FinishedAt: at(500*time.Millisecond + 9*40*time.Millisecond), OutputTokens: 10},
			want: map[string]float64{"attained=true,objective=ttft,pool=p,priority=1": 1, "attained=true,objective=tpot,pool=p,priority=1": 1},
		},
		{
			name: "ttft missed tpot met",
			obj:  framework.Objective{Priority: 1, TTFT: time.Second, TPOT: 50 * time.Millisecond},
			resp: framework.Response{StartedAt: start, FirstTokenAt: at(2 * time.Second), FinishedAt: at(2*time.Second + 40*time.Millisecond), OutputTokens: 2},
			want: map[string]float64{"attained=false,objective=ttft,pool=p,priority=1": 1, "attained=true,objective=tpot,pool=p,priority=1": 1},
		},
		{
			name: "failed request misses every objective",
			obj:  framework.Objective{Priority: 0, TTFT: time.Second, TPOT: time.Second},
			resp: framework.Response{StartedAt: start, FirstTokenAt: at(time.Millisecond), FinishedAt: at(2 * time.Millisecond), OutputTokens: 2, Err: errors.New("reset")},
			want: map[string]float64{"attained=false,objective=ttft,pool=p,priority=0": 1, "attained=false,objective=tpot,pool=p,priority=0": 1},
		},
		{
			name: "no token misses ttft",
			obj:  framework.Objective{Priority: 2, TTFT: time.Second},
			resp: framework.Response{StartedAt: start, FinishedAt: at(time.Millisecond)},
			want: map[string]float64{"attained=false,objective=ttft,pool=p,priority=2": 1},
		},
		{
			name: "no objective records nothing",
			resp: framework.Response{StartedAt: start, FirstTokenAt: at(time.Millisecond), OutputTokens: 1},
			want: map[string]float64{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, preg := newRegistry(t)
			r.ObserveObjectives("p", tc.obj, &tc.resp)
			got := values(t, preg, "nebu_objective_attained_total")
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestHandlerServesExemplars(t *testing.T) {
	r, _ := newRegistry(t)
	tid, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	sid, _ := trace.SpanIDFromHex("0102030405060708")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled}))
	r.ObserveRequest(ctx, Request{Model: "m", Pool: "p", Endpoint: "e", Status: 200, Duration: 2 * time.Second, TTFT: time.Second, OutputTokens: 1})

	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/openmetrics-text") {
		t.Fatalf("content type %q", ct)
	}
	line := lineWithPrefix(text, `nebu_request_duration_seconds_bucket{endpoint="e",fairness_id="",model="m",pool="p",priority="0",le="2.56"} 1 # {`)
	for _, part := range []string{`trace_id="0102030405060708090a0b0c0d0e0f10"`, `span_id="0102030405060708"`, `} 2.0 `} {
		if !strings.Contains(line, part) {
			t.Fatalf("duration bucket exemplar %q lacks %s in:\n%s", line, part, text)
		}
	}
	if lineWithPrefix(text, `nebu_request_total{endpoint="e",fairness_id="",model="m",pool="p",priority="0",status="200"} 1`) == "" {
		t.Fatalf("request_total missing:\n%s", text)
	}
}

func lineWithPrefix(text, prefix string) string {
	for l := range strings.Lines(text) {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}
