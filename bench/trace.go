// Package bench records, synthesizes and replays gateway request traces and reports latency and throughput per scheduler configuration
package bench

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// One chat message of a traced request
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Image URLs or data URLs sent after the text
	Images []string `json:"images,omitempty"`
}

// One request of a trace, sent at its offset from the trace start
type Request struct {
	// Milliseconds after the trace start the request arrives
	OffsetMS float64 `json:"offset_ms"`
	// Model name the client sends, the gateway default when empty
	Model string `json:"model,omitempty"`
	// Adapter the model name resolved to on its pool, empty for the base model
	Adapter string `json:"adapter,omitempty"`
	// Conversation sent as is, exclusive with Prompt
	Messages []Message `json:"messages,omitempty"`
	// Prompt text sent as one user message, exclusive with Messages
	Prompt string `json:"prompt,omitempty"`
	// Prompt length in tokens, the only prompt when neither Messages nor Prompt is set
	PromptTokens int `json:"prompt_tokens,omitempty"`
	// Output token bound, the server default when zero
	MaxTokens int  `json:"max_tokens,omitempty"`
	Stream    bool `json:"stream"`
	// Value of the objective header, a named objective or an integer priority
	Objective string `json:"objective,omitempty"`
	// Value of the fairness id header
	FairnessID string `json:"fairness_id,omitempty"`
}

// Arrival time after the trace start
func (r Request) Offset() time.Duration {
	return time.Duration(r.OffsetMS * float64(time.Millisecond))
}

// Refuses a request replay cannot send
func (r Request) validate() error {
	switch {
	case math.IsNaN(r.OffsetMS) || math.IsInf(r.OffsetMS, 0) || r.OffsetMS < 0:
		return fmt.Errorf("offset_ms %v is not a finite non-negative number", r.OffsetMS)
	case len(r.Messages) > 0 && r.Prompt != "":
		return errors.New("messages and prompt are both set")
	case len(r.Messages) == 0 && r.Prompt == "" && r.PromptTokens <= 0:
		return errors.New("no messages, prompt or prompt_tokens")
	case r.PromptTokens < 0 || r.MaxTokens < 0:
		return errors.New("prompt_tokens and max_tokens must not be negative")
	}
	for i, m := range r.Messages {
		if m.Role == "" {
			return fmt.Errorf("message %d has no role", i)
		}
		if slices.Contains(m.Images, "") {
			return fmt.Errorf("message %d has an empty image", i)
		}
	}
	return nil
}

// Parses JSONL requests, one object per line with blank lines ignored
func Read(r io.Reader) ([]Request, error) {
	br := bufio.NewReader(r)
	var out []Request
	for n := 1; ; n++ {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			dec := json.NewDecoder(bytes.NewReader(line))
			dec.DisallowUnknownFields()
			var req Request
			if derr := dec.Decode(&req); derr != nil {
				return nil, fmt.Errorf("line %d: %w", n, derr)
			}
			if verr := req.validate(); verr != nil {
				return nil, fmt.Errorf("line %d: %w", n, verr)
			}
			out = append(out, req)
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// Reads a JSONL trace file
func ReadFile(path string) ([]Request, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reqs, err := Read(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return reqs, nil
}

// Writes requests as JSONL, one object per line
func Write(w io.Writer, reqs []Request) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	for i, r := range reqs {
		if err := r.validate(); err != nil {
			return fmt.Errorf("request %d: %w", i, err)
		}
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// Writes requests to a JSONL file, replacing it
func WriteFile(path string, reqs []Request) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := Write(f, reqs); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Distribution kinds a token count is drawn from
const (
	DistFixed       = "fixed"
	DistUniform     = "uniform"
	DistNormal      = "normal"
	DistLogNormal   = "lognormal"
	DistExponential = "exponential"
)

// A token count distribution, every draw rounded and clamped to one through Max
type Dist struct {
	Kind string
	// Fixed value, uniform low bound, or the mean of the other kinds
	A float64
	// Uniform high bound, normal standard deviation, or lognormal sigma of the log
	B float64
	// Largest draw, unbounded when zero
	Max int
}

// Parses a kind and its numbers joined by colons, such as uniform then 64 then 512
func ParseDist(s string) (Dist, error) {
	parts := strings.Split(s, ":")
	d := Dist{Kind: parts[0]}
	want := map[string]int{DistFixed: 1, DistUniform: 2, DistNormal: 2, DistLogNormal: 2, DistExponential: 1}[d.Kind]
	if want == 0 {
		return d, fmt.Errorf("distribution %q has unknown kind %q", s, d.Kind)
	}
	if len(parts)-1 != want {
		return d, fmt.Errorf("distribution %q takes %d numbers after %s", s, want, d.Kind)
	}
	vals := make([]float64, want)
	for i, p := range parts[1:] {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return d, fmt.Errorf("distribution %q: %w", s, err)
		}
		vals[i] = v
	}
	d.A = vals[0]
	if want == 2 {
		d.B = vals[1]
	}
	return d, d.validate()
}

func (d Dist) String() string {
	switch d.Kind {
	case DistFixed, DistExponential:
		return d.Kind + ":" + strconv.FormatFloat(d.A, 'g', -1, 64)
	}
	return d.Kind + ":" + strconv.FormatFloat(d.A, 'g', -1, 64) + ":" + strconv.FormatFloat(d.B, 'g', -1, 64)
}

// Refuses parameters a draw cannot use
func (d Dist) validate() error {
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	if !finite(d.A) || !finite(d.B) || d.Max < 0 {
		return fmt.Errorf("distribution %s has a non finite parameter or negative max", d)
	}
	switch d.Kind {
	case DistFixed, DistExponential:
		if d.A < 1 {
			return fmt.Errorf("distribution %s needs a value of at least 1", d)
		}
	case DistUniform:
		if d.A < 1 || d.B < d.A {
			return fmt.Errorf("distribution %s needs 1 <= low <= high", d)
		}
	case DistNormal, DistLogNormal:
		if d.A < 1 || d.B < 0 {
			return fmt.Errorf("distribution %s needs a mean of at least 1 and a non-negative spread", d)
		}
	default:
		return fmt.Errorf("distribution kind %q is unknown", d.Kind)
	}
	return nil
}

// Draws one count, rounded and clamped to one through Max
func (d Dist) draw(rng *rand.Rand) int {
	var v float64
	switch d.Kind {
	case DistFixed:
		v = d.A
	case DistUniform:
		v = d.A + rng.Float64()*(d.B-d.A)
	case DistNormal:
		v = d.A + rng.NormFloat64()*d.B
	case DistLogNormal:
		mu := math.Log(d.A) - d.B*d.B/2
		v = math.Exp(mu + d.B*rng.NormFloat64())
	case DistExponential:
		v = rng.ExpFloat64() * d.A
	}
	n := max(1, int(math.Round(v)))
	if d.Max > 0 {
		n = min(n, d.Max)
	}
	return n
}

// What a synthesized trace looks like
type Spec struct {
	Requests int
	// Mean Poisson arrivals per second
	Rate  float64
	Model string
	// Total prompt words, a prefixed prompt taking at least one word past its prefix
	PromptTokens Dist
	OutputTokens Dist
	// Fraction of requests that open with one of the shared prefixes as a system message
	PrefixShare  float64
	Prefixes     int
	PrefixTokens int
	// Distinct fairness ids, none when zero
	FairnessIDs int
	// Zipf exponent over fairness ids, uniform when zero
	FairnessSkew float64
	// Objective header values assigned uniformly, none when empty
	Objectives []string
	// Adapters a share of requests name in place of the model
	Adapters     []string
	AdapterShare float64
}

// Refuses a spec Synthesize cannot honor
func (s Spec) validate() error {
	unit := func(v float64) bool { return v >= 0 && v <= 1 }
	switch {
	case s.Requests < 1:
		return errors.New("synthesize needs at least one request")
	case !(s.Rate > 0) || math.IsInf(s.Rate, 0):
		return errors.New("synthesize needs a finite positive arrival rate")
	case !unit(s.PrefixShare) || !unit(s.AdapterShare):
		return errors.New("prefix and adapter shares must lie in 0 to 1")
	case s.PrefixShare > 0 && (s.Prefixes < 1 || s.PrefixTokens < 1):
		return errors.New("a prefix share needs at least one prefix of at least one token")
	case s.PrefixShare > 0 && s.PromptTokens.Max > 0 && s.PrefixTokens >= s.PromptTokens.Max:
		return fmt.Errorf("prefixes of %d tokens leave no room in prompts of at most %d", s.PrefixTokens, s.PromptTokens.Max)
	case s.AdapterShare > 0 && len(s.Adapters) == 0:
		return errors.New("an adapter share needs adapter names")
	case s.FairnessIDs < 0 || !(s.FairnessSkew >= 0) || math.IsInf(s.FairnessSkew, 0):
		return errors.New("fairness ids and skew must be finite and non-negative")
	}
	if err := s.PromptTokens.validate(); err != nil {
		return fmt.Errorf("prompt tokens: %w", err)
	}
	if err := s.OutputTokens.validate(); err != nil {
		return fmt.Errorf("output tokens: %w", err)
	}
	for _, a := range s.Adapters {
		if a == "" {
			return errors.New("adapter names must not be empty")
		}
	}
	return nil
}

// Words prompts are built from, each one stub token
var vocabulary = strings.Fields(`able acid aged also area away baby back ball band bank base bath bear beat been beer bell belt best bill bird blow blue boat body bond bone book boom born boss both bowl bulk burn bush busy call calm came camp card care case cash cast cell chat chip city club coal coat code cold come cook cool cope copy core cost crew crop dark data date dawn days deal dear debt deep deny desk dial diet disc disk does done door dose down draw drew drop dual dust duty each earn ease east easy edge else even ever exit face fact fail fair fall farm fast fate fear feed feel feet fell felt file fill film find fine fire firm fish five flat flow food foot form fort four free from fuel full fund gain game gate gave gear gene gift girl give glad goal goes gold golf gone good grew grey grow gulf hair half hall hand hang hard have head hear heat held help here hero high hill hire hold hole home hope host hour huge hung hunt idea inch into iron item join jump jury just keen keep kept kick kind king knee knew know lack lady laid lake land lane last late lead left less life lift like line link list live load loan lock logo long look lose loss lost love luck made mail main make male many mark mass meal mean meat meet menu mere mile milk mill mind mine miss mode mood moon more most move much must name near neck need news next nice nine none nose note okay once only onto open oral over pace pack page paid pain pair palm park part pass past path peak pick pink pipe plan play plot plug plus poll pool poor port post pull pure push race rail rain rank rare rate read real rear rely rent rest rice rich ride ring rise risk road rock role roll roof room root rose rule rush safe said sake sale salt same sand save seat seed seek seem seen self sell send sent ship shop shot show shut side sign site size skin slip slow snow soft soil sold sole some song soon sort soul spot star stay step stop such suit sure take tale talk tall tank tape task team tech tell tend term test text than that them then they thin this thus till time tiny told toll tone took tool tour town tree trip true tune turn twin type unit upon used user vary vast very vice view vote wage wait wake walk wall want ward warm wash wave ways weak wear week well went were west what when whom wide wife wild will wind wine wing wire wise wish with wood word wore work yard yeah year your zero zone`)

// Space separated words drawn from the vocabulary
func words(rng *rand.Rand, n int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(vocabulary[rng.IntN(len(vocabulary))])
	}
	return b.String()
}

// Index drawn with weight one over rank to the power skew
func zipf(rng *rand.Rand, cum []float64) int {
	x := rng.Float64() * cum[len(cum)-1]
	for i, c := range cum {
		if x < c {
			return i
		}
	}
	return len(cum) - 1
}

// Generates a trace with Poisson arrivals, drawn prompt and output lengths, shared prefixes, fairness ids, objectives and adapters
func Synthesize(seed uint64, spec Spec) ([]Request, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	prefixes := make([]string, spec.Prefixes)
	for i := range prefixes {
		prefixes[i] = words(rng, spec.PrefixTokens)
	}
	var cum []float64
	for i := range spec.FairnessIDs {
		w := math.Pow(float64(i+1), -spec.FairnessSkew)
		if len(cum) > 0 {
			w += cum[len(cum)-1]
		}
		cum = append(cum, w)
	}
	out := make([]Request, spec.Requests)
	at := 0.0
	for i := range out {
		if i > 0 {
			at += rng.ExpFloat64() / spec.Rate * 1000
		}
		r := Request{OffsetMS: at, Model: spec.Model, Stream: true, MaxTokens: spec.OutputTokens.draw(rng)}
		total := spec.PromptTokens.draw(rng)
		if spec.PrefixShare > 0 && rng.Float64() < spec.PrefixShare {
			own := max(1, total-spec.PrefixTokens)
			r.Messages = []Message{{Role: "system", Content: prefixes[rng.IntN(len(prefixes))]}, {Role: "user", Content: words(rng, own)}}
			r.PromptTokens = spec.PrefixTokens + own
		} else {
			r.Messages = []Message{{Role: "user", Content: words(rng, total)}}
			r.PromptTokens = total
		}
		if spec.AdapterShare > 0 && rng.Float64() < spec.AdapterShare {
			r.Adapter = spec.Adapters[rng.IntN(len(spec.Adapters))]
			r.Model = r.Adapter
		}
		if len(cum) > 0 {
			r.FairnessID = "tenant-" + strconv.Itoa(zipf(rng, cum))
		}
		if len(spec.Objectives) > 0 {
			r.Objective = spec.Objectives[rng.IntN(len(spec.Objectives))]
		}
		out[i] = r
	}
	return out, nil
}
