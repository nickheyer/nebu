package bench

import (
	"bytes"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestJSONLRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		reqs []Request
	}{
		{"messages", []Request{{OffsetMS: 0, Model: "m", Messages: []Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "hi <b>&</b>"}}, MaxTokens: 8, Stream: true}}},
		{"prompt", []Request{{OffsetMS: 12.5, Prompt: "tell me a story", Objective: "interactive", FairnessID: "tenant-1"}}},
		{"images", []Request{{OffsetMS: 1, Messages: []Message{{Role: "user", Content: "what is this", Images: []string{"https://x/a.png", "data:image/png;base64,AAAA"}}}}}},
		{"token count", []Request{{OffsetMS: 3, PromptTokens: 512, MaxTokens: 64, Adapter: "lora-a", Model: "lora-a"}}},
		{"several", []Request{{OffsetMS: 0, Prompt: "a"}, {OffsetMS: 1, Prompt: "b", Stream: true}, {OffsetMS: 1, PromptTokens: 2, Objective: "-3"}}},
		{"empty", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := Write(&buf, tc.reqs); err != nil {
				t.Fatal(err)
			}
			if n := strings.Count(buf.String(), "\n"); n != len(tc.reqs) {
				t.Fatalf("wrote %d lines for %d requests", n, len(tc.reqs))
			}
			got, err := Read(&buf)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.reqs) {
				t.Fatalf("round trip changed requests\nwant %+v\ngot  %+v", tc.reqs, got)
			}
		})
	}
}

func TestFileRoundTrip(t *testing.T) {
	reqs := []Request{{OffsetMS: 0, Prompt: "x"}, {OffsetMS: 5, PromptTokens: 9, Stream: true}}
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := WriteFile(path, reqs); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, reqs) {
		t.Fatalf("got %+v", got)
	}
}

func TestReadSkipsBlankLinesAndTakesNoTrailingNewline(t *testing.T) {
	got, err := Read(strings.NewReader("\n{\"offset_ms\":1,\"prompt\":\"a\",\"stream\":false}\n\n   \n{\"offset_ms\":2,\"prompt_tokens\":3,\"stream\":true}"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Request{{OffsetMS: 1, Prompt: "a"}, {OffsetMS: 2, PromptTokens: 3, Stream: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestReadRefuses(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"bad json", `{"offset_ms":`, "line 1"},
		{"unknown field", `{"offset_ms":0,"prompt":"a","stream":true,"temperature":1}`, "unknown field"},
		{"negative offset", `{"offset_ms":-1,"prompt":"a","stream":true}`, "offset_ms"},
		{"no prompt", `{"offset_ms":0,"stream":true}`, "no messages"},
		{"both prompts", `{"offset_ms":0,"prompt":"a","messages":[{"role":"user","content":"b"}],"stream":true}`, "both set"},
		{"no role", `{"offset_ms":0,"messages":[{"content":"b"}],"stream":true}`, "no role"},
		{"empty image", `{"offset_ms":0,"messages":[{"role":"user","content":"b","images":[""]}],"stream":true}`, "empty image"},
		{"negative tokens", `{"offset_ms":0,"prompt":"a","max_tokens":-2,"stream":true}`, "negative"},
		{"second line", "{\"offset_ms\":0,\"prompt\":\"a\",\"stream\":true}\n{\"offset_ms\":0,\"stream\":true}", "line 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Read(strings.NewReader(tc.input))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestWriteRefusesInvalidRequest(t *testing.T) {
	if err := Write(&bytes.Buffer{}, []Request{{OffsetMS: math.NaN(), Prompt: "a"}}); err == nil {
		t.Fatal("a NaN offset was written")
	}
}

func TestParseDist(t *testing.T) {
	cases := []struct {
		in   string
		want Dist
		bad  bool
	}{
		{in: "fixed:128", want: Dist{Kind: DistFixed, A: 128}},
		{in: "uniform:64:512", want: Dist{Kind: DistUniform, A: 64, B: 512}},
		{in: "normal:256:64", want: Dist{Kind: DistNormal, A: 256, B: 64}},
		{in: "lognormal:512:0.8", want: Dist{Kind: DistLogNormal, A: 512, B: 0.8}},
		{in: "exponential:200", want: Dist{Kind: DistExponential, A: 200}},
		{in: "fixed", bad: true},
		{in: "fixed:1:2", bad: true},
		{in: "zipf:3", bad: true},
		{in: "uniform:9:3", bad: true},
		{in: "fixed:0", bad: true},
		{in: "normal:10:-1", bad: true},
		{in: "fixed:abc", bad: true},
		{in: "fixed:Inf", bad: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseDist(tc.in)
			if tc.bad {
				if err == nil {
					t.Fatalf("parsed %+v", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v", got, err)
			}
			if got.String() != tc.in {
				t.Fatalf("String gives %q", got.String())
			}
		})
	}
}

func TestDistDrawStaysInBounds(t *testing.T) {
	rng := newTestRand(7)
	cases := []struct {
		d        Dist
		lo, hi   int
		meanNear float64
	}{
		{Dist{Kind: DistFixed, A: 40}, 40, 40, 40},
		{Dist{Kind: DistUniform, A: 10, B: 20}, 10, 20, 15},
		{Dist{Kind: DistNormal, A: 100, B: 10}, 1, 1 << 30, 100},
		{Dist{Kind: DistLogNormal, A: 100, B: 0.5}, 1, 1 << 30, 100},
		{Dist{Kind: DistExponential, A: 50}, 1, 1 << 30, 50},
		{Dist{Kind: DistExponential, A: 50, Max: 60}, 1, 60, 0},
	}
	for _, tc := range cases {
		t.Run(tc.d.String(), func(t *testing.T) {
			sum := 0
			const n = 20000
			for range n {
				v := tc.d.draw(rng)
				if v < tc.lo || v > tc.hi {
					t.Fatalf("draw %d outside %d to %d", v, tc.lo, tc.hi)
				}
				sum += v
			}
			if mean := float64(sum) / n; tc.meanNear > 0 && math.Abs(mean-tc.meanNear) > tc.meanNear*0.05 {
				t.Fatalf("mean %v, want near %v", mean, tc.meanNear)
			}
		})
	}
}

// A spec touching every synthesized feature
func fullSpec() Spec {
	return Spec{
		Requests: 400, Rate: 20, Model: "bench-model",
		PromptTokens: Dist{Kind: DistUniform, A: 32, B: 256}, OutputTokens: Dist{Kind: DistFixed, A: 16},
		PrefixShare: 0.5, Prefixes: 3, PrefixTokens: 24,
		FairnessIDs: 4, FairnessSkew: 1.2, Objectives: []string{"interactive", "batch"},
		Adapters: []string{"lora-a", "lora-b"}, AdapterShare: 0.25,
	}
}

func TestSynthesizeIsDeterministicBySeed(t *testing.T) {
	a, err := Synthesize(42, fullSpec())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Synthesize(42, fullSpec())
	c, _ := Synthesize(43, fullSpec())
	if !reflect.DeepEqual(a, b) {
		t.Fatal("one seed gave two traces")
	}
	if reflect.DeepEqual(a, c) {
		t.Fatal("two seeds gave one trace")
	}
	var wa, wb bytes.Buffer
	if err := Write(&wa, a); err != nil {
		t.Fatal(err)
	}
	Write(&wb, b)
	if !bytes.Equal(wa.Bytes(), wb.Bytes()) {
		t.Fatal("one seed wrote two different files")
	}
}

func TestSynthesizeArrivalRate(t *testing.T) {
	for _, rate := range []float64{2, 50, 400} {
		spec := Spec{Requests: 20000, Rate: rate, PromptTokens: Dist{Kind: DistFixed, A: 1}, OutputTokens: Dist{Kind: DistFixed, A: 1}}
		reqs, err := Synthesize(1, spec)
		if err != nil {
			t.Fatal(err)
		}
		if reqs[0].OffsetMS != 0 {
			t.Fatalf("first arrival at %v", reqs[0].OffsetMS)
		}
		gaps := make([]float64, 0, len(reqs)-1)
		for i := 1; i < len(reqs); i++ {
			g := reqs[i].OffsetMS - reqs[i-1].OffsetMS
			if g < 0 {
				t.Fatalf("arrival %d precedes the one before it", i)
			}
			gaps = append(gaps, g)
		}
		got := float64(len(reqs)-1) / (reqs[len(reqs)-1].OffsetMS / 1000)
		if math.Abs(got-rate)/rate > 0.03 {
			t.Fatalf("rate %v gave %v arrivals per second", rate, got)
		}
		mean, sq := 0.0, 0.0
		for _, g := range gaps {
			mean += g
		}
		mean /= float64(len(gaps))
		for _, g := range gaps {
			sq += (g - mean) * (g - mean)
		}
		if cv := math.Sqrt(sq/float64(len(gaps))) / mean; math.Abs(cv-1) > 0.05 {
			t.Fatalf("rate %v gaps have coefficient of variation %v, a Poisson process gives 1", rate, cv)
		}
	}
}

func TestSynthesizeShape(t *testing.T) {
	spec := fullSpec()
	spec.Requests = 4000
	reqs, err := Synthesize(9, spec)
	if err != nil {
		t.Fatal(err)
	}
	prefixed, adapted := 0, 0
	tenants := map[string]int{}
	prefixes := map[string]bool{}
	for i, r := range reqs {
		if err := r.validate(); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if !r.Stream || r.MaxTokens != 16 {
			t.Fatalf("request %d stream %v max tokens %d", i, r.Stream, r.MaxTokens)
		}
		words := 0
		for _, m := range r.Messages {
			words += len(strings.Fields(m.Content))
		}
		if words != r.PromptTokens {
			t.Fatalf("request %d counts %d prompt tokens over %d words", i, r.PromptTokens, words)
		}
		if r.Messages[0].Role == "system" {
			prefixed++
			prefixes[r.Messages[0].Content] = true
			if len(strings.Fields(r.Messages[0].Content)) != spec.PrefixTokens {
				t.Fatalf("request %d prefix has %d words", i, len(strings.Fields(r.Messages[0].Content)))
			}
		}
		if r.Adapter != "" {
			adapted++
			if r.Model != r.Adapter || !slices.Contains(spec.Adapters, r.Adapter) {
				t.Fatalf("request %d model %q adapter %q", i, r.Model, r.Adapter)
			}
		} else if r.Model != spec.Model {
			t.Fatalf("request %d model %q", i, r.Model)
		}
		if !slices.Contains(spec.Objectives, r.Objective) {
			t.Fatalf("request %d objective %q", i, r.Objective)
		}
		tenants[r.FairnessID]++
	}
	near := func(name string, got int, share float64) {
		if want := share * float64(len(reqs)); math.Abs(float64(got)-want) > want*0.1 {
			t.Fatalf("%s %d of %d, want near %v", name, got, len(reqs), want)
		}
	}
	near("prefixed", prefixed, spec.PrefixShare)
	near("adapted", adapted, spec.AdapterShare)
	if len(prefixes) != spec.Prefixes {
		t.Fatalf("%d distinct prefixes, want %d", len(prefixes), spec.Prefixes)
	}
	if len(tenants) != spec.FairnessIDs {
		t.Fatalf("tenants %v", tenants)
	}
	if !(tenants["tenant-0"] > tenants["tenant-1"] && tenants["tenant-1"] > tenants["tenant-3"]) {
		t.Fatalf("skewed tenants not ordered by rank, %v", tenants)
	}
}

func TestSynthesizeRefuses(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Spec)
	}{
		{"no requests", func(s *Spec) { s.Requests = 0 }},
		{"no rate", func(s *Spec) { s.Rate = 0 }},
		{"infinite rate", func(s *Spec) { s.Rate = math.Inf(1) }},
		{"share above one", func(s *Spec) { s.PrefixShare = 1.5 }},
		{"prefix share without prefixes", func(s *Spec) { s.Prefixes = 0 }},
		{"adapter share without adapters", func(s *Spec) { s.Adapters = nil }},
		{"empty adapter", func(s *Spec) { s.Adapters = []string{""} }},
		{"negative skew", func(s *Spec) { s.FairnessSkew = -1 }},
		{"bad prompt dist", func(s *Spec) { s.PromptTokens = Dist{Kind: "zipf", A: 1} }},
		{"bad output dist", func(s *Spec) { s.OutputTokens = Dist{} }},
		{"prefix past the longest prompt", func(s *Spec) { s.PromptTokens.Max = s.PrefixTokens }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fullSpec()
			tc.edit(&s)
			if _, err := Synthesize(1, s); err == nil {
				t.Fatal("spec accepted")
			}
		})
	}
}
