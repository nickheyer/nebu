// Package metrics defines the Prometheus families nebu exposes and typed methods that record into them
package metrics

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/datalayer/scrape"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

// Prefix of every family name
const namespace = "nebu"

// Label names shared across families
const (
	lModel      = "model"
	lPool       = "pool"
	lEndpoint   = "endpoint"
	lPriority   = "priority"
	lFairnessID = "fairness_id"
	lStatus     = "status"
	lReason     = "reason"
	lProfile    = "profile"
	lFilter     = "filter"
	lScorer     = "scorer"
	lObjective  = "objective"
	lAttained   = "attained"
)

// Objective label values
const (
	ObjectiveTTFT = "ttft"
	ObjectiveTPOT = "tpot"
)

// Every family, registered on a registry the caller owns
type Registry struct {
	gatherer prometheus.Gatherer

	requests        *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	ttft            *prometheus.HistogramVec
	tpot            *prometheus.HistogramVec
	queueWait       *prometheus.HistogramVec
	promptTokens    *prometheus.HistogramVec
	outputTokens    *prometheus.HistogramVec
	shed            *prometheus.CounterVec
	queueDepth      *prometheus.GaugeVec

	pickDuration  *prometheus.HistogramVec
	filterDropped *prometheus.CounterVec
	score         *prometheus.HistogramVec
	fallback      *prometheus.CounterVec

	endpointQueued  *prometheus.GaugeVec
	endpointRunning *prometheus.GaugeVec
	endpointKV      *prometheus.GaugeVec
	endpointAssumed *prometheus.GaugeVec
	endpointReady   *prometheus.GaugeVec
	scrapeDuration  *prometheus.HistogramVec
	scrapeErrors    *prometheus.CounterVec
	poolReady       *prometheus.GaugeVec
	poolKV          *prometheus.GaugeVec
	poolQueued      *prometheus.GaugeVec
	objective       *prometheus.CounterVec
	prefixHitRatio  *prometheus.HistogramVec
	prefixEntries   prometheus.Gauge
	fairnessPrompt  *prometheus.CounterVec
	fairnessOutput  *prometheus.CounterVec
}

func counter(name, help string, labels ...string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: name, Help: help}, labels)
}

func gauge(name, help string, labels ...string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: namespace, Name: name, Help: help}, labels)
}

// A histogram exposing classic buckets and a native sparse histogram for scrapers that negotiate it
func histogram(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace:                       namespace,
		Name:                            name,
		Help:                            help,
		Buckets:                         buckets,
		NativeHistogramBucketFactor:     1.1,
		NativeHistogramMaxBucketNumber:  160,
		NativeHistogramMinResetDuration: time.Hour,
	}, labels)
}

// Creates every family and registers it on reg, failing on any collision
func New(reg *prometheus.Registry) (*Registry, error) {
	secs := prometheus.ExponentialBuckets(0.005, 2, 16)
	fine := prometheus.ExponentialBuckets(0.001, 2, 16)
	tokens := prometheus.ExponentialBuckets(1, 2, 18)
	unit := prometheus.LinearBuckets(0, 0.1, 11)

	r := &Registry{
		gatherer: reg,

		requests:        counter("request_total", "Requests answered", lModel, lPool, lEndpoint, lPriority, lFairnessID, lStatus),
		requestDuration: histogram("request_duration_seconds", "End to end request latency", secs, lModel, lPool, lEndpoint, lPriority, lFairnessID),
		ttft:            histogram("request_ttft_seconds", "Time to first token", secs, lModel, lPool, lEndpoint, lPriority, lFairnessID),
		tpot:            histogram("request_tpot_seconds", "Time per output token after the first", fine, lModel, lPool, lEndpoint, lPriority, lFairnessID),
		queueWait:       histogram("request_queue_wait_seconds", "Time an admitted request spent in flow control before dispatch", fine, lModel, lPool, lEndpoint, lPriority, lFairnessID),
		promptTokens:    histogram("request_prompt_tokens", "Prompt tokens of the first round per request", tokens, lModel, lPool, lEndpoint, lPriority, lFairnessID),
		outputTokens:    histogram("request_output_tokens", "Output tokens per request", tokens, lModel, lPool, lEndpoint, lPriority, lFairnessID),
		shed:            counter("request_shed_total", "Requests flow control or policy did not dispatch", lPool, lPriority, lReason),
		queueDepth:      gauge("flow_queue_depth", "Requests waiting in flow control", lPool, lPriority),

		pickDuration:  histogram("scheduler_pick_duration_seconds", "Time one scheduler profile took to pick", prometheus.ExponentialBuckets(0.00001, 2, 16), lPool, lProfile),
		filterDropped: counter("scheduler_filter_dropped_total", "Endpoints a filter removed", lPool, lProfile, lFilter),
		score:         histogram("scheduler_score", "Scores a scorer gave endpoints", unit, lPool, lProfile, lScorer),
		fallback:      counter("scheduler_fallback_total", "Requests routed without the primary scheduling path", lPool, lReason),

		endpointQueued:  gauge("endpoint_queued_requests", "Requests the model server reports queued", lPool, lEndpoint),
		endpointRunning: gauge("endpoint_running_requests", "Requests the model server reports running", lPool, lEndpoint),
		endpointKV:      gauge("endpoint_kv_cache_utilization", "KV cache utilization the model server reports, zero to one", lPool, lEndpoint),
		endpointAssumed: gauge("endpoint_assumed_requests", "Requests this gateway dispatched beyond the scraped counts", lPool, lEndpoint),
		endpointReady:   gauge("endpoint_ready", "One when the endpoint is ready and healthy", lPool, lEndpoint),
		scrapeDuration:  histogram("endpoint_scrape_duration_seconds", "Time one metrics scrape took", prometheus.ExponentialBuckets(0.001, 2, 14), lPool),
		scrapeErrors:    counter("endpoint_scrape_errors_total", "Metrics scrapes that failed", lPool, lEndpoint),
		poolReady:       gauge("pool_ready_endpoints", "Endpoints of the pool that are ready and healthy", lPool),
		poolKV:          gauge("pool_kv_cache_utilization", "Mean KV cache utilization over the pool's endpoints reporting metrics, zero to one", lPool),
		poolQueued:      gauge("pool_queued_requests", "Requests queued at the pool's model servers, summed over endpoints reporting metrics", lPool),
		objective:       counter("objective_attained_total", "Latency objectives met or missed", lPool, lPriority, lObjective, lAttained),
		prefixHitRatio:  histogram("prefix_cache_hit_ratio", "Fraction of the prompt the picked endpoint already held", unit, lPool),
		prefixEntries:   prometheus.NewGauge(prometheus.GaugeOpts{Namespace: namespace, Name: "prefix_cache_entries", Help: "Entries in the prefix cache index"}),
		fairnessPrompt:  counter("fairness_prompt_tokens_total", "Prompt tokens used per fairness id, the basis of fairness and quotas", lFairnessID),
		fairnessOutput:  counter("fairness_output_tokens_total", "Output tokens used per fairness id, the basis of fairness and quotas", lFairnessID),
	}
	all := []prometheus.Collector{
		r.requests, r.requestDuration, r.ttft, r.tpot, r.queueWait, r.promptTokens, r.outputTokens, r.shed, r.queueDepth,
		r.pickDuration, r.filterDropped, r.score, r.fallback,
		r.endpointQueued, r.endpointRunning, r.endpointKV, r.endpointAssumed, r.endpointReady,
		r.scrapeDuration, r.scrapeErrors, r.poolReady, r.poolKV, r.poolQueued, r.objective, r.prefixHitRatio, r.prefixEntries,
		r.fairnessPrompt, r.fairnessOutput,
	}
	for i, c := range all {
		if err := reg.Register(c); err != nil {
			for _, done := range all[:i] {
				reg.Unregister(done)
			}
			return nil, err
		}
	}
	return r, nil
}

// Serves the registry in the Prometheus text or OpenMetrics format, the latter carrying exemplars
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.gatherer, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

// Observes v with the sampled trace of ctx as exemplar when one is present
func observe(ctx context.Context, o prometheus.Observer, v float64) {
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() && sc.IsSampled() {
		o.(prometheus.ExemplarObserver).ObserveWithExemplar(v, prometheus.Labels{"trace_id": sc.TraceID().String(), "span_id": sc.SpanID().String()})
		return
	}
	o.Observe(v)
}

func prio(p int) string { return strconv.Itoa(p) }

// One finished request as the gateway answered it
type Request struct {
	Model      string
	Pool       datalayer.PoolID
	Endpoint   datalayer.EndpointID
	Priority   int
	FairnessID string
	// The HTTP status answered to the client
	Status   int
	Duration time.Duration
	// Zero when no token arrived
	TTFT time.Duration
	// Zero when fewer than two tokens arrived
	TPOT time.Duration
	// Prompt tokens of the first round, tool rounds left out
	PromptTokens int
	OutputTokens int
	// Whether flow control admitted the request, its queue wait then observed
	Admitted  bool
	QueueWait time.Duration
}

// Counts the request and observes its latencies, queue wait and token counts, linking the trace in ctx as exemplar
func (r *Registry) ObserveRequest(ctx context.Context, q Request) {
	labels := []string{q.Model, string(q.Pool), string(q.Endpoint), prio(q.Priority), q.FairnessID}
	r.requests.WithLabelValues(append(labels, strconv.Itoa(q.Status))...).Inc()
	observe(ctx, r.requestDuration.WithLabelValues(labels...), q.Duration.Seconds())
	if q.TTFT > 0 {
		observe(ctx, r.ttft.WithLabelValues(labels...), q.TTFT.Seconds())
	}
	if q.TPOT > 0 {
		observe(ctx, r.tpot.WithLabelValues(labels...), q.TPOT.Seconds())
	}
	if q.Admitted {
		observe(ctx, r.queueWait.WithLabelValues(labels...), q.QueueWait.Seconds())
	}
	r.promptTokens.WithLabelValues(labels...).Observe(float64(q.PromptTokens))
	r.outputTokens.WithLabelValues(labels...).Observe(float64(q.OutputTokens))
	r.fairnessPrompt.WithLabelValues(q.FairnessID).Add(float64(q.PromptTokens))
	r.fairnessOutput.WithLabelValues(q.FairnessID).Add(float64(q.OutputTokens))
}

// Counts a request that was not dispatched, with the reason it was turned away
func (r *Registry) Shed(pool datalayer.PoolID, priority int, reason string) {
	r.shed.WithLabelValues(string(pool), prio(priority), reason).Inc()
}

// Counts each admission flow control did not dispatch under its outcome
func (r *Registry) OnAdmit(pool datalayer.PoolID, band int, _ time.Duration, outcome string) {
	if outcome != flowcontrol.OutcomeDispatched {
		r.Shed(pool, band, outcome)
	}
}

var _ flowcontrol.Observer = (*Registry)(nil)

// Observes how long one scheduler profile took to pick for a request
func (r *Registry) ObservePick(pool datalayer.PoolID, profile string, d time.Duration) {
	r.pickDuration.WithLabelValues(string(pool), profile).Observe(d.Seconds())
}

// Counts the endpoints one filter removed in a profile run
func (r *Registry) FilterDropped(pool datalayer.PoolID, profile, filter string, dropped int) {
	r.filterDropped.WithLabelValues(string(pool), profile, filter).Add(float64(dropped))
}

// Observes one score a scorer gave an endpoint in a profile run
func (r *Registry) ObserveScore(pool datalayer.PoolID, profile, scorer string, score float64) {
	r.score.WithLabelValues(string(pool), profile, scorer).Observe(score)
}

var _ framework.Observer = (*Registry)(nil)

// Counts a request routed by a fallback path with the reason the primary path was left
func (r *Registry) SchedulerFallback(pool datalayer.PoolID, reason string) {
	r.fallback.WithLabelValues(string(pool), reason).Inc()
}

// Observes one metrics scrape of an endpoint under its pool, counting it as an error when err is set
func (r *Registry) OnScrape(pool datalayer.PoolID, endpoint datalayer.EndpointID, d time.Duration, err error) {
	r.scrapeDuration.WithLabelValues(string(pool)).Observe(d.Seconds())
	if err != nil {
		r.scrapeErrors.WithLabelValues(string(pool), string(endpoint)).Inc()
	}
}

var _ scrape.Observer = (*Registry)(nil)

// Counts each latency objective the request set as attained or missed, failed requests missing every one
func (r *Registry) ObserveObjectives(pool datalayer.PoolID, obj framework.Objective, resp *framework.Response) {
	if obj.TTFT > 0 {
		t := resp.TTFT()
		r.objectiveOutcome(pool, obj.Priority, ObjectiveTTFT, resp.Err == nil && t > 0 && t <= obj.TTFT)
	}
	if obj.TPOT > 0 {
		r.objectiveOutcome(pool, obj.Priority, ObjectiveTPOT, resp.Err == nil && resp.OutputTokens > 0 && resp.TPOT() <= obj.TPOT)
	}
}

func (r *Registry) objectiveOutcome(pool datalayer.PoolID, priority int, objective string, attained bool) {
	r.objective.WithLabelValues(string(pool), prio(priority), objective, strconv.FormatBool(attained)).Inc()
}

// Observes the fraction of a prompt the picked endpoint already held in its prefix cache
func (r *Registry) ObservePrefixHitRatio(pool datalayer.PoolID, ratio float64) {
	r.prefixHitRatio.WithLabelValues(string(pool)).Observe(ratio)
}

// Sets the number of entries the prefix cache index holds
func (r *Registry) SetPrefixCacheEntries(n int) { r.prefixEntries.Set(float64(n)) }
