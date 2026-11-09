package latency

import (
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// A linear latency law in milliseconds with Gaussian noise
type law struct {
	w     vec
	sigma float64
}

// Noise free latency in milliseconds
func (l law) mean(f Features) float64 {
	x := features(f)
	return dot(&l.w, &x)
}

// A noisy latency drawn from the law
func (l law) draw(r *rand.Rand, f Features) time.Duration {
	return millis(max(l.mean(f)+r.NormFloat64()*l.sigma, 1e-3))
}

// Request features spread over a busy server's range
func randomFeatures(r *rand.Rand) Features {
	prompt := r.IntN(8000)
	return Features{
		PromptTokens:       prompt,
		CachedTokens:       r.IntN(prompt/2 + 1),
		QueuedRequests:     r.IntN(20),
		RunningRequests:    r.IntN(48),
		AssumedRequests:    r.IntN(4),
		KVCacheUtilization: r.Float64(),
	}
}

var (
	ttftLaw = law{vec{40, 120, 25, 3, 60, 0.8}, 6}
	tpotLaw = law{vec{8, 0.5, 0.2, 0.6, 15, 0.05}, 0.8}
)

// Trains the endpoint on n requests whose latencies follow the laws
func train(b *Bayes, r *rand.Rand, ep datalayer.EndpointID, ttftL, tpotL law, n int) {
	for range n {
		f := randomFeatures(r)
		b.Observe(ep, f, ttftL.draw(r, f), tpotL.draw(r, f))
	}
}

// Fails unless predictions track the law within tolerance and their spread covers fresh draws
func checkTracks(t *testing.T, r *rand.Rand, name string, predict func(Features) Prediction, l law) {
	t.Helper()
	const probes = 400
	var absErr float64
	covered := 0
	for range probes {
		f := randomFeatures(r)
		p := predict(f)
		truth := l.mean(f)
		err := math.Abs(ms(p.Mean) - truth)
		absErr += err
		if err > 4*l.sigma {
			t.Fatalf("%s predicts %.2fms for %.2fms", name, ms(p.Mean), truth)
		}
		sd := ms(p.StdDev)
		if sd < 0.5*l.sigma || sd > 2*l.sigma {
			t.Fatalf("%s spread %.3fms for noise %.3fms", name, sd, l.sigma)
		}
		if math.Abs(ms(l.draw(r, f))-ms(p.Mean)) <= 2*sd {
			covered++
		}
	}
	if mean := absErr / probes; mean > l.sigma {
		t.Fatalf("%s mean error %.3fms over noise %.3fms", name, mean, l.sigma)
	}
	if share := float64(covered) / probes; share < 0.85 {
		t.Fatalf("%s two sigma band covers %.2f of draws", name, share)
	}
}

func TestLearnsLinearLaw(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"defaults", Config{}},
		{"slow forgetting", Config{Forgetting: 0.999, NoiseDecay: 0.01}},
		{"tight prior", Config{PriorVariance: 1e4}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := New(c.cfg)
			r := rand.New(rand.NewPCG(1, 2))
			train(b, r, "e", ttftLaw, tpotLaw, 4000)
			checkTracks(t, r, "ttft", func(f Features) Prediction { return b.PredictTTFT("e", f) }, ttftLaw)
			checkTracks(t, r, "tpot", func(f Features) Prediction { return b.PredictTPOT("e", f) }, tpotLaw)
			if p := b.PredictTTFT("e", Features{}); p.Samples != 4000 {
				t.Fatalf("%d samples, want 4000", p.Samples)
			}
		})
	}
}

func TestTracksDrift(t *testing.T) {
	shifted := func(l law, scale, offset float64) law {
		out := l
		for i := range out.w {
			out.w[i] *= scale
		}
		out.w[0] += offset
		return out
	}
	cases := []struct {
		name          string
		before, after law
	}{
		{"slower server", ttftLaw, shifted(ttftLaw, 1.8, 150)},
		{"faster server", ttftLaw, shifted(ttftLaw, 0.5, -10)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := New(Config{})
			r := rand.New(rand.NewPCG(3, 4))
			train(b, r, "e", c.before, tpotLaw, 3000)
			train(b, r, "e", c.after, tpotLaw, 400)
			checkTracks(t, r, "ttft", func(f Features) Prediction { return b.PredictTTFT("e", f) }, c.after)
		})
	}
}

func TestPoolPriorBeforeMinSamples(t *testing.T) {
	cold := law{vec{400, 10, 1, 1, 1, 0}, 3}
	for _, k := range []int{1, 5, 12} {
		t.Run(fmt.Sprintf("min samples %d", k), func(t *testing.T) {
			b := New(Config{MinSamples: k})
			r := rand.New(rand.NewPCG(5, uint64(k)))
			train(b, r, "warm", ttftLaw, tpotLaw, 600)
			probe := randomFeatures(r)
			for n := 0; n <= k; n++ {
				got := b.PredictTTFT("cold", probe)
				pool := b.PredictTTFT("never seen", probe)
				switch {
				case n < k && got != pool:
					t.Fatalf("after %d samples predicted %+v, pool %+v", n, got, pool)
				case n < k && got.Samples != 600+n:
					t.Fatalf("after %d samples pool reports %d samples", n, got.Samples)
				case n == k && got.Samples != k:
					t.Fatalf("after %d samples the endpoint reports %d samples", n, got.Samples)
				case n == k && got == pool:
					t.Fatalf("after %d samples the endpoint still predicts from the pool", n)
				}
				f := randomFeatures(r)
				b.Observe("cold", f, cold.draw(r, f), tpotLaw.draw(r, f))
			}
		})
	}
}

func TestPoolPriorSensibleForNewEndpoint(t *testing.T) {
	b := New(Config{})
	r := rand.New(rand.NewPCG(6, 7))
	laws := make([]law, 8)
	var spread float64
	for i := range laws {
		laws[i] = ttftLaw
		offset := float64(i*10 - 35)
		laws[i].w[0] += offset
		spread += offset * offset / float64(len(laws))
	}
	for i := range 4000 {
		f := randomFeatures(r)
		e := i % len(laws)
		b.Observe(datalayer.EndpointID(fmt.Sprintf("e%d", e)), f, laws[e].draw(r, f), tpotLaw.draw(r, f))
	}
	pooled := law{ttftLaw.w, math.Sqrt(ttftLaw.sigma*ttftLaw.sigma + spread)}
	checkTracks(t, r, "new endpoint", func(f Features) Prediction { return b.PredictTTFT("new", f) }, pooled)
}

func TestForget(t *testing.T) {
	b := New(Config{})
	r := rand.New(rand.NewPCG(8, 9))
	train(b, r, "pool", ttftLaw, tpotLaw, 200)
	train(b, r, "gone", law{vec{900, 0, 0, 0, 0, 0}, 2}, tpotLaw, 50)
	probe := randomFeatures(r)
	if b.PredictTTFT("gone", probe) == b.PredictTTFT("never seen", probe) {
		t.Fatalf("trained endpoint predicts from the pool")
	}
	b.Forget("gone")
	b.Forget("never seen")
	if got, pool := b.PredictTTFT("gone", probe), b.PredictTTFT("never seen", probe); got != pool {
		t.Fatalf("forgotten endpoint predicts %+v, pool %+v", got, pool)
	}
}

func TestUnmeasuredLatencies(t *testing.T) {
	cases := []struct {
		name       string
		ttft, tpot time.Duration
		wantTTFT   int
		wantTPOT   int
	}{
		{"both measured", 80 * time.Millisecond, 9 * time.Millisecond, 1, 1},
		{"single token output", 80 * time.Millisecond, 0, 1, 0},
		{"failed before a token", 0, 0, 0, 0},
		{"negative ignored", -time.Millisecond, 9 * time.Millisecond, 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := New(Config{MinSamples: 1})
			f := Features{PromptTokens: 100}
			b.Observe("e", f, c.ttft, c.tpot)
			if got := b.PredictTTFT("e", f).Samples; got != c.wantTTFT {
				t.Fatalf("ttft samples %d, want %d", got, c.wantTTFT)
			}
			if got := b.PredictTPOT("e", f).Samples; got != c.wantTPOT {
				t.Fatalf("tpot samples %d, want %d", got, c.wantTPOT)
			}
		})
	}
}

func TestPriorPredictive(t *testing.T) {
	cfg := Config{PriorVariance: 4, NoiseVariance: 9}
	b := New(cfg)
	f := Features{PromptTokens: 3000, CachedTokens: 1000, QueuedRequests: 2, RunningRequests: 3, AssumedRequests: 1, KVCacheUtilization: 0.5}
	x := features(f)
	want := millis(math.Sqrt(9 + 4*dot(&x, &x)))
	p := b.PredictTTFT("e", f)
	if p.Mean != 0 || p.Samples != 0 || absDuration(p.StdDev-want) > time.Nanosecond {
		t.Fatalf("prior predictive %+v, want zero mean and spread %v", p, want)
	}
}

func TestNoiselessStaysFinite(t *testing.T) {
	exact := law{ttftLaw.w, 0}
	b := New(Config{})
	r := rand.New(rand.NewPCG(10, 11))
	f := Features{PromptTokens: 1000, RunningRequests: 1}
	for range 40000 {
		b.Observe("e", f, millis(exact.mean(f)), millis(exact.mean(f)))
	}
	train(b, r, "e", exact, exact, 2000)
	for range 200 {
		g := randomFeatures(r)
		p := b.PredictTTFT("e", g)
		if math.Abs(ms(p.Mean)-exact.mean(g)) > 0.01 || p.StdDev < 0 || p.StdDev > time.Millisecond {
			t.Fatalf("noiseless prediction %+v for %.4fms", p, exact.mean(g))
		}
	}
}

func TestConcurrentObservePredict(t *testing.T) {
	b := New(Config{MinSamples: 3})
	eps := []datalayer.EndpointID{"a", "b", "c", "d"}
	var wg sync.WaitGroup
	for w := range 12 {
		wg.Go(func() {
			r := rand.New(rand.NewPCG(uint64(w), 12))
			for i := range 4000 {
				ep := eps[r.IntN(len(eps))]
				f := randomFeatures(r)
				switch i % 7 {
				case 0, 1, 2:
					b.Observe(ep, f, ttftLaw.draw(r, f), tpotLaw.draw(r, f))
				case 3:
					b.Forget(ep)
				default:
					for _, p := range []Prediction{b.PredictTTFT(ep, f), b.PredictTPOT(ep, f)} {
						if p.Mean < 0 || p.StdDev <= 0 {
							t.Errorf("prediction %+v", p)
						}
					}
				}
			}
		})
	}
	wg.Wait()
	r := rand.New(rand.NewPCG(13, 14))
	checkTracks(t, r, "pool after concurrent training", func(f Features) Prediction { return b.PredictTTFT("never seen", f) }, ttftLaw)
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func BenchmarkObserve(b *testing.B) {
	p := New(Config{})
	r := rand.New(rand.NewPCG(1, 1))
	fs := make([]Features, 1024)
	for i := range fs {
		fs[i] = randomFeatures(r)
	}
	b.Run("serial", func(b *testing.B) {
		i := 0
		for b.Loop() {
			f := fs[i%len(fs)]
			p.Observe("e", f, 120*time.Millisecond, 9*time.Millisecond)
			i++
		}
	})
	eps := make([]datalayer.EndpointID, 64)
	for i := range eps {
		eps[i] = datalayer.EndpointID(fmt.Sprint(i))
	}
	b.Run("parallel", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				p.Observe(eps[i%len(eps)], fs[i%len(fs)], 120*time.Millisecond, 9*time.Millisecond)
				i++
			}
		})
	})
}

func BenchmarkPredict(b *testing.B) {
	p := New(Config{})
	r := rand.New(rand.NewPCG(2, 2))
	eps := make([]datalayer.EndpointID, 64)
	for i := range eps {
		eps[i] = datalayer.EndpointID(fmt.Sprint(i))
		train(p, r, eps[i], ttftLaw, tpotLaw, 50)
	}
	fs := make([]Features, 1024)
	for i := range fs {
		fs[i] = randomFeatures(r)
	}
	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			p.PredictTTFT(eps[i%len(eps)], fs[i%len(fs)])
			i++
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				p.PredictTTFT(eps[i%len(eps)], fs[i%len(fs)])
				i++
			}
		})
	})
}

func TestPriorCenterDecidesUnmeasured(t *testing.T) {
	ms := time.Millisecond
	b := New(Config{PriorTTFT: 300 * ms, PriorTPOT: 20 * ms, MinSamples: 3})
	f := Features{PromptTokens: 500}
	tests := []struct {
		name string
		got  Prediction
		mean time.Duration
	}{
		{"ttft from the prior", b.PredictTTFT("cold", f), 300 * ms},
		{"tpot from the prior", b.PredictTPOT("cold", f), 20 * ms},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got.Mean != tc.mean || tc.got.Confident || tc.got.Samples != 0 {
				t.Fatalf("unmeasured prediction %+v, want mean %v and no confidence", tc.got, tc.mean)
			}
		})
	}
	pt, pp := b.Prior(f)
	if pt.Mean != 300*ms || pp.Mean != 20*ms || pt.StdDev != 10*ms || pt.Confident {
		t.Fatalf("prior %+v %+v", pt, pp)
	}
	for i := range 3 {
		b.Observe("warm", f, 100*ms, 5*ms)
		if p := b.PredictTTFT("warm", f); p.Confident != (i == 2) {
			t.Fatalf("after %d samples confident %v", i+1, p.Confident)
		}
	}
	if p := b.PredictTTFT("warm", f); !p.Confident || absDuration(p.Mean-100*ms) > 20*ms {
		t.Fatalf("learned prediction %+v", p)
	}
}

// Confidence needs the mean's variance at the request's features under the noise, so unspanned features stay prior decided
func TestConfidenceNeedsSpannedFeatures(t *testing.T) {
	b := New(Config{MinSamples: 2})
	idle := Features{PromptTokens: 500}
	for range 10 {
		b.Observe("e", idle, 100*time.Millisecond, 5*time.Millisecond)
	}
	if p := b.PredictTTFT("e", idle); !p.Confident {
		t.Fatalf("ten samples at the same features not confident: %+v", p)
	}
	if p := b.PredictTTFT("e", Features{PromptTokens: 500, QueuedRequests: 40, KVCacheUtilization: 0.9}); p.Confident {
		t.Fatalf("features never seen reported confident: %+v", p)
	}
}

// Load test cache hits on e02 before the 69 hour prediction, n none, p 581, f 600 tokens
const recordedCached = "" +
	"nfpfppppppfffppppfppppfppffpppffpfffpppppppfppppfffppppfpffffppffpfppffpfpffpffpffffpffpffpffppf" +
	"ffffffppfpffppppfpfpfppfpffpppffffffffppppffpfpfpppfffffffpfffppffffpppffffffffffpppfffffpffpfpf" +
	"fppfpffpppfpfffffffpffpfpfffpffffpfpfpfpffpfpfpfpffpfpffffffpppppppffppfffffppfffpffffppffpfffff" +
	"fpfffpfpfppppffffpfpffffpffffffpfppffffpfppffpfffpffppppfpfppfppfpffpfffpffffffppfffpfffpfpffppf" +
	"ffffpfffpfffffpfpppffffppfpffppppppfpffpffffpfffppfffppfpfppppfpppfppfpfppfffppfpfffffffpfpfff"

// The load test endpoint i, its scraped counts fixed and its prompt cache hit read from the recording
func loadTestFeatures(i int, hit byte, assumed int) Features {
	cached := map[byte]int{'n': 0, 'p': 581, 'f': 600}[hit]
	return Features{PromptTokens: 600, CachedTokens: cached, QueuedRequests: i % 5, RunningRequests: i % 7, KVCacheUtilization: float64(i%10) / 10, AssumedRequests: assumed}
}

// An endpoint whose queued and running counts are both i and whose KV utilization is i tenths
func lockstepFeatures(i int, hit byte) Features {
	f := loadTestFeatures(0, hit, 0)
	f.QueuedRequests, f.RunningRequests, f.KVCacheUtilization = i, i, float64(i)/10
	return f
}

// Euclidean norm of every weight but the intercept
func slopeNorm(w vec) float64 {
	var s float64
	for _, v := range w[1:] {
		s += v * v
	}
	return math.Sqrt(s)
}

// Collinear scraped counts keep constant latencies predicted at their value everywhere and keep slopes within the law's
func TestCollinearFeaturesStayBounded(t *testing.T) {
	var everyEndpoint []Features
	for i := range 8 {
		for _, hit := range []byte("npf") {
			for _, assumed := range []int{0, 3, 23} {
				everyEndpoint = append(everyEndpoint, loadTestFeatures(i, hit, assumed))
			}
		}
	}
	lockstep := func(ep datalayer.EndpointID) []Features {
		var out []Features
		for i := 1; i <= 4; i++ {
			if ep == "never seen" || ep == datalayer.EndpointID(fmt.Sprintf("e0%d", i)) {
				for _, hit := range []byte("npf") {
					out = append(out, lockstepFeatures(i, hit))
				}
			}
		}
		return out
	}
	cases := []struct {
		name       string
		steps      int
		at         func(j int) (datalayer.EndpointID, Features)
		ttft, tpot law
		probes     func(datalayer.EndpointID) []Features
		constant   bool
		tol        float64
	}{
		{
			name:     "recorded load test sequence on one endpoint",
			steps:    len(recordedCached),
			at:       func(j int) (datalayer.EndpointID, Features) { return "e02", loadTestFeatures(2, recordedCached[j], 0) },
			ttft:     law{vec{80}, 0},
			tpot:     law{vec{ms(19393939)}, 0},
			probes:   func(datalayer.EndpointID) []Features { return everyEndpoint },
			constant: true,
			tol:      0.01,
		},
		{
			name:  "noiseless law over endpoints whose queued running and kv move together",
			steps: 2000,
			at: func(j int) (datalayer.EndpointID, Features) {
				i := j%4 + 1
				return datalayer.EndpointID(fmt.Sprintf("e0%d", i)), lockstepFeatures(i, "npf"[j/4%3])
			},
			ttft:   law{vec{50, 30, 10}, 0},
			tpot:   law{vec{10, 1, 0, 0.5}, 0},
			probes: lockstep,
			tol:    0.01,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := New(Config{})
			laws := [2]law{ttft: c.ttft, tpot: c.tpot}
			seen := map[datalayer.EndpointID]bool{}
			var worst, worstSlope float64
			var worstAt, worstSlopeAt string
			check := func(step int) {
				for _, ep := range append(slices.Collect(maps.Keys(seen)), "never seen") {
					for _, f := range c.probes(ep) {
						for k, p := range [2]Prediction{ttft: b.PredictTTFT(ep, f), tpot: b.PredictTPOT(ep, f)} {
							want := laws[k].mean(f)
							if dev := math.Abs(ms(p.Mean) - want); dev > worst {
								worst, worstAt = dev, fmt.Sprintf("after %d observations %s kind %d predicts %v at %+v, observed %.6fms", step, ep, k, p.Mean, f, want)
							}
						}
					}
				}
			}
			for j := range c.steps {
				ep, f := c.at(j)
				seen[ep] = true
				b.Observe(ep, f, millis(laws[ttft].mean(f)), millis(laws[tpot].mean(f)))
				for k := range laws {
					w := b.snap.Load().mean[k]
					if over := slopeNorm(w) - slopeNorm(laws[k].w)*(1+1e-9); over > worstSlope {
						worstSlope, worstSlopeAt = over, fmt.Sprintf("after %d observations kind %d pool weights %v, law %v", j+1, k, w, laws[k].w)
					}
				}
				if c.constant {
					check(j + 1)
				}
			}
			check(c.steps)
			if worst > c.tol {
				t.Errorf("worst prediction off by %.6gms, %s", worst, worstAt)
			}
			if worstSlope > 1e-9 {
				t.Errorf("slope norm over the law's by %.6g, %s", worstSlope, worstSlopeAt)
			}
		})
	}
}

// Zero, negative, non finite and over one config fields all fall back to the defaults
func TestWithDefaultsReplacesOutOfRange(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	want := Config{PriorVariance: DefaultPriorVariance, NoiseVariance: DefaultNoiseVariance, NoiseDecay: DefaultNoiseDecay, Forgetting: DefaultForgetting, MinSamples: DefaultMinSamples}
	cases := []struct {
		name string
		cfg  Config
	}{
		{"zero", Config{}},
		{"negative", Config{PriorVariance: -1, NoiseVariance: -1, NoiseDecay: -1, Forgetting: -1, MinSamples: -1}},
		{"not a number", Config{PriorVariance: nan, NoiseVariance: nan, NoiseDecay: nan, Forgetting: nan}},
		{"infinite", Config{PriorVariance: inf, NoiseVariance: inf, NoiseDecay: inf, Forgetting: inf}},
		{"over one", Config{NoiseDecay: 1.5, Forgetting: 1.5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := withDefaults(c.cfg); got != want {
				t.Fatalf("defaults %+v, want %+v", got, want)
			}
		})
	}
}

// Millisecond counts round to the nanosecond and saturate at the longest duration instead of wrapping negative
func TestMillisSaturates(t *testing.T) {
	cases := []struct {
		ms   float64
		want time.Duration
	}{
		{80, 80 * time.Millisecond},
		{ms(19393939), 19393939},
		{9e12, 9e18},
		{1e13, math.MaxInt64},
		{math.Inf(1), math.MaxInt64},
	}
	for _, c := range cases {
		if got := millis(c.ms); got != c.want {
			t.Fatalf("millis(%g) = %d, want %d", c.ms, got, c.want)
		}
	}
	b := New(Config{})
	for q := range 20 {
		f := Features{QueuedRequests: q}
		b.Observe("e", f, millis(10+float64(q)), time.Millisecond)
	}
	if p := b.PredictTTFT("e", Features{QueuedRequests: 1 << 62}); p.Mean != math.MaxInt64 {
		t.Fatalf("prediction at an enormous queue %v, want the longest duration", p.Mean)
	}
}
