package latency

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// Weights per model, over intercept, fresh kilotokens, queued, running, KV utilization and fresh kilotokens times running
const dims = 6

// Weights after the intercept, learned on regressors centered at their discounted means
const slopes = dims - 1

// Values a zero or out of range config field takes
const (
	DefaultPriorVariance = 1e6
	DefaultNoiseVariance = 100
	DefaultNoiseDecay    = 0.05
	DefaultForgetting    = 0.98
	DefaultMinSamples    = 5
)

// Variance of a duration measured in whole nanoseconds, in squared milliseconds, the floor of any noise estimate
const quantizationVariance = 1e-12 / 12

// Predictor configuration
type Config struct {
	// Prior variance of the latency at the mean features and of every slope, at the starting noise
	PriorVariance float64
	// Noise variance in squared milliseconds before any residual is seen
	NoiseVariance float64
	// Weight of the newest residual in the exponentially weighted noise variance, in (0, 1]
	NoiseDecay float64
	// Factor discounting every older sample at each observation, in (0, 1], one never forgetting
	Forgetting float64
	// Samples an endpoint learns before its own model replaces the pool wide one
	MinSamples int
	// Time to first token the prior centers on before any sample, zero meaning an unmeasured endpoint answers at once
	PriorTTFT time.Duration
	// Time per output token the prior centers on before any sample
	PriorTPOT time.Duration
}

// Replaces zero, non finite or out of range fields with their defaults
func withDefaults(cfg Config) Config {
	if !(cfg.PriorVariance > 0 && cfg.PriorVariance < math.Inf(1)) {
		cfg.PriorVariance = DefaultPriorVariance
	}
	if !(cfg.NoiseVariance > 0 && cfg.NoiseVariance < math.Inf(1)) {
		cfg.NoiseVariance = DefaultNoiseVariance
	}
	if !(cfg.NoiseDecay > 0 && cfg.NoiseDecay <= 1) {
		cfg.NoiseDecay = DefaultNoiseDecay
	}
	if !(cfg.Forgetting > 0 && cfg.Forgetting <= 1) {
		cfg.Forgetting = DefaultForgetting
	}
	if cfg.MinSamples <= 0 {
		cfg.MinSamples = DefaultMinSamples
	}
	return cfg
}

type vec [dims]float64

// Regressors or weights past the intercept
type svec [slopes]float64

// Lower triangular Cholesky matrix of a slope precision in units of the noise
type factor [slopes][slopes]float64

// Regressors for one request, prompt tokens in thousands so every weight stays near unit scale
func features(f Features) vec {
	fresh := float64(max(f.PromptTokens-f.CachedTokens, 0)) / 1000
	load := float64(f.RunningRequests + f.AssumedRequests)
	return vec{1, fresh, float64(f.QueuedRequests), load, f.KVCacheUtilization, fresh * load}
}

// Factor, centering, noise and sample count a prediction reads
type posterior struct {
	l factor
	// Discounted mean of the regressors, one in the intercept slot
	center vec
	// Discounted sample count and the prior precision of every weight, both in units of the noise
	weight, prec float64
	noise        float64
	n            int
}

// Predictive mean and spread at x, confident once minSamples are learned and the mean's own variance at x is under the noise
func (p *posterior) predict(x, mean *vec, minSamples int) Prediction {
	mu := dot(mean, x)
	v := p.noise * p.leverage(x)
	sd := math.Sqrt(p.noise + v)
	return Prediction{Mean: millis(max(mu, 0)), StdDev: millis(sd), Samples: p.n, Confident: p.n >= minSamples && v <= p.noise}
}

// Posterior variance of the mean response at x in units of the noise, the intercept share plus the slopes share
func (p *posterior) leverage(x *vec) float64 {
	d := p.offset(x)
	z := forward(&p.l, &d)
	return 1/(p.prec+p.weight) + norm2(&z)
}

// Regressors past the intercept less their discounted means
func (p *posterior) offset(x *vec) svec {
	var d svec
	for i := range d {
		d[i] = x[i+1] - p.center[i+1]
	}
	return d
}

// One online conjugate Bayesian linear regression on centered regressors with stabilized forgetting of its data
type model struct {
	posterior
	// Discounted mean latency
	ybar float64
	// Discounted centered sums of d dT and d times the latency
	xx [slopes][slopes]float64
	xy svec
}

// Weights at the prior with the configured starting noise
func newModel(cfg Config) model {
	m := model{posterior: posterior{center: vec{1}, prec: cfg.NoiseVariance / cfg.PriorVariance, noise: cfg.NoiseVariance}}
	m.refactor()
	return m
}

// Weights the posterior centers on given the prior center, the intercept carried from the mean regressors back to zero
func (m *model) mean(prior *vec) vec {
	at := dot(prior, &m.center)
	var rhs svec
	for i := range rhs {
		rhs[i] = m.prec*prior[i+1] + m.xy[i]
	}
	s := solve(&m.l, &rhs)
	w := vec{at + (m.ybar-at)*m.weight/(m.prec+m.weight)}
	for i, v := range s {
		w[0] -= v * m.center[i+1]
		w[i+1] = v
	}
	return w
}

// Updates noise from the one step ahead residual, then discounts and adds the sample around the running means
func (m *model) observe(x *vec, y float64, prior *vec, cfg Config) {
	mu := m.mean(prior)
	r := y - dot(&mu, x)
	m.noise = max((1-cfg.NoiseDecay)*m.noise+cfg.NoiseDecay*r*r/(1+m.leverage(x)), quantizationVariance)
	kept := cfg.Forgetting * m.weight
	m.weight = kept + 1
	share := kept / m.weight
	d := m.offset(x)
	dy := y - m.ybar
	for i := range slopes {
		for j := range i + 1 {
			m.xx[i][j] = cfg.Forgetting*m.xx[i][j] + share*d[i]*d[j]
		}
		m.xy[i] = cfg.Forgetting*m.xy[i] + share*d[i]*dy
		m.center[i+1] += d[i] / m.weight
	}
	m.ybar += dy / m.weight
	m.n++
	m.refactor()
}

// Cholesky factor of prior precision plus centered scatter, pivots floored at the prior precision every pivot provably exceeds
func (m *model) refactor() {
	for i := range slopes {
		for j := range i + 1 {
			s := m.xx[i][j]
			if i == j {
				s += m.prec
			}
			for k := range j {
				s -= m.l[i][k] * m.l[j][k]
			}
			if i == j {
				m.l[i][i] = math.Sqrt(max(s, m.prec))
			} else {
				m.l[i][j] = s / m.l[j][j]
			}
		}
	}
}

// Solves L z equals b
func forward(l *factor, b *svec) svec {
	var z svec
	for i := range slopes {
		s := b[i]
		for k := range i {
			s -= l[i][k] * z[k]
		}
		z[i] = s / l[i][i]
	}
	return z
}

// Two triangular passes giving w where L LT w equals b
func solve(l *factor, b *svec) svec {
	z := forward(l, b)
	var w svec
	for i := slopes - 1; i >= 0; i-- {
		s := z[i]
		for k := i + 1; k < slopes; k++ {
			s -= l[k][i] * w[k]
		}
		w[i] = s / l[i][i]
	}
	return w
}

func dot(a, b *vec) float64 {
	var s float64
	for i := range dims {
		s += a[i] * b[i]
	}
	return s
}

// Sum of squares
func norm2(a *svec) float64 {
	var s float64
	for _, v := range a {
		s += v * v
	}
	return s
}

// Converts a millisecond count to a duration rounded to the nanosecond, saturating at the longest duration
func millis(ms float64) time.Duration {
	ns := math.Round(ms * 1e6)
	if ns >= math.MaxInt64 {
		return math.MaxInt64
	}
	return time.Duration(ns)
}

// Which latency a model predicts
type kind int

const (
	ttft kind = iota
	tpot
)

// The TTFT and TPOT models of one endpoint under its own lock
type endpointModels struct {
	mu     sync.Mutex
	models [2]model
}

// An immutable copy of the pool wide models published after every observation
type poolSnapshot struct {
	post [2]posterior
	mean [2]vec
}

// Online Bayesian linear regression per endpoint backed by a pool wide model, implementing Predictor
type Bayes struct {
	cfg Config
	// Prior centers of the pool models, the configured latency on the intercept and zero on every slope
	prior [2]vec
	eps   sync.Map

	poolMu sync.Mutex
	pool   [2]model
	snap   atomic.Pointer[poolSnapshot]
}

var _ Predictor = (*Bayes)(nil)

// Builds a predictor whose every model starts at the zero mean prior
func New(cfg Config) *Bayes {
	cfg = withDefaults(cfg)
	b := &Bayes{cfg: cfg}
	b.prior[ttft][0] = ms(max(cfg.PriorTTFT, 0))
	b.prior[tpot][0] = ms(max(cfg.PriorTPOT, 0))
	for k := range b.pool {
		b.pool[k] = newModel(cfg)
	}
	b.publish()
	return b
}

// Copies the pool models and their weight means into a fresh snapshot
func (b *Bayes) publish() {
	var s poolSnapshot
	for k := range b.pool {
		s.post[k] = b.pool[k].posterior
		s.mean[k] = b.pool[k].mean(&b.prior[k])
	}
	b.snap.Store(&s)
}

// The prior's own answer for the request on any endpoint, what an unmeasured endpoint is judged by
func (b *Bayes) Prior(f Features) (ttftP, tpotP Prediction) {
	x := features(f)
	return Prediction{Mean: millis(dot(&b.prior[ttft], &x)), StdDev: millis(math.Sqrt(b.cfg.NoiseVariance))},
		Prediction{Mean: millis(dot(&b.prior[tpot], &x)), StdDev: millis(math.Sqrt(b.cfg.NoiseVariance))}
}

// Time to first token for the request on the endpoint
func (b *Bayes) PredictTTFT(endpoint datalayer.EndpointID, f Features) Prediction {
	return b.predict(endpoint, f, ttft)
}

// Time per output token for the request on the endpoint
func (b *Bayes) PredictTPOT(endpoint datalayer.EndpointID, f Features) Prediction {
	return b.predict(endpoint, f, tpot)
}

// The endpoint's posterior predictive once it has MinSamples samples, the pool model's before
func (b *Bayes) predict(endpoint datalayer.EndpointID, f Features, k kind) Prediction {
	x := features(f)
	s := b.snap.Load()
	if v, ok := b.eps.Load(endpoint); ok {
		em := v.(*endpointModels)
		em.mu.Lock()
		m := &em.models[k]
		if m.n >= b.cfg.MinSamples {
			mean := m.mean(&s.mean[k])
			p := m.predict(&x, &mean, b.cfg.MinSamples)
			em.mu.Unlock()
			return p
		}
		em.mu.Unlock()
	}
	return s.post[k].predict(&x, &s.mean[k], b.cfg.MinSamples)
}

// Learns each positive latency on the endpoint and the pool, a non positive one meaning it was never measured
func (b *Bayes) Observe(endpoint datalayer.EndpointID, f Features, ttftD, tpotD time.Duration) {
	ys := [2]time.Duration{ttft: ttftD, tpot: tpotD}
	if ys[ttft] <= 0 && ys[tpot] <= 0 {
		return
	}
	x := features(f)
	s := b.snap.Load()
	em := b.models(endpoint)
	em.mu.Lock()
	for k, d := range ys {
		if d > 0 {
			em.models[k].observe(&x, ms(d), &s.mean[k], b.cfg)
		}
	}
	em.mu.Unlock()
	b.poolMu.Lock()
	for k, d := range ys {
		if d > 0 {
			b.pool[k].observe(&x, ms(d), &b.prior[k], b.cfg)
		}
	}
	b.publish()
	b.poolMu.Unlock()
}

// The endpoint's TTFT and TPOT regressions, created at the prior on first use
func (b *Bayes) models(endpoint datalayer.EndpointID) *endpointModels {
	if v, ok := b.eps.Load(endpoint); ok {
		return v.(*endpointModels)
	}
	fresh := &endpointModels{models: [2]model{newModel(b.cfg), newModel(b.cfg)}}
	v, _ := b.eps.LoadOrStore(endpoint, fresh)
	return v.(*endpointModels)
}

// Drops the endpoint's models so it predicts from the pool again
func (b *Bayes) Forget(endpoint datalayer.EndpointID) { b.eps.Delete(endpoint) }

// A duration in milliseconds
func ms(d time.Duration) float64 { return float64(d) / 1e6 }
