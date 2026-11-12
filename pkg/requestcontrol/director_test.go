package requestcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/flowcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

func bodyModel(t *testing.T, b []byte) string {
	t.Helper()
	var v struct{ Model string }
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v.Model
}

func TestModelPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		headers    []string
		secondPool bool
		target     string
		mutated    string
		err        error
	}{
		{"body", chat("llama"), nil, false, "llama", "", nil},
		{"header over body", chat("other"), []string{HeaderModelName, "llama-lora"}, false, "llama-lora", "llama-lora", nil},
		{"header without body", "", []string{HeaderModelName, "llama"}, false, "llama", "", nil},
		{"default without model", `{"messages":[]}`, nil, false, "llama", "llama", nil},
		{"default without body", "", nil, false, "llama", "", nil},
		{"no default with two ready pools", `{"messages":[]}`, nil, true, "", "", ErrNoModel},
		{"unknown model", chat("gpt"), nil, false, "", "", ErrUnknownModel},
		{"rewrite header over body", chat("llama"), []string{HeaderModelRewrite, "llama-lora"}, false, "llama-lora", "llama-lora", nil},
		{"rewrite header to unknown", chat("llama"), []string{HeaderModelRewrite, "nope"}, false, "", "", ErrUnknownModel},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			if c.secondPool {
				e.store.PutPool(datalayer.Pool{ID: "p2", Model: "mistral"})
				e.store.UpsertEndpoint(endpoint("e9", "p2", "10.0.1.1", true))
			}
			dec, err := e.dir.Decide(context.Background(), inbound(t, c.body, c.headers...))
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("err %v, want %v", err, c.err)
				}
				if e.flow.admitted.Load() != 0 {
					t.Fatal("a refused request reached flow control")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if dec.TargetModel != c.target || dec.Request.TargetModel != c.target || dec.Headers[HeaderModelName] != c.target {
				t.Fatalf("target %q request %q header %q, want %q", dec.TargetModel, dec.Request.TargetModel, dec.Headers[HeaderModelName], c.target)
			}
			switch {
			case c.mutated == "" && c.body != "" && dec.Body == nil:
				t.Fatal("the decision dropped the body")
			case c.mutated == "" && c.body == "" && dec.Body != nil:
				t.Fatalf("a bodiless request decided with body %s", dec.Body.Bytes())
			case c.mutated != "" && bodyModel(t, dec.Body.Bytes()) != c.mutated:
				t.Fatalf("decided body %s, want model %q", dec.Body.Bytes(), c.mutated)
			}
			wantAdapter := ""
			if c.target == "llama-lora" {
				wantAdapter = "llama-lora"
			}
			if dec.Adapter != wantAdapter || dec.Request.Adapter != wantAdapter {
				t.Fatalf("adapter %q, want %q", dec.Adapter, wantAdapter)
			}
			e.dir.ResponseComplete(context.Background(), dec, &framework.Response{})
			e.settled(t)
		})
	}
}

func TestCatchAllToUnservedModelIsNotDefaulted(t *testing.T) {
	e := newEnv(t)
	if err := e.table.SetRules("s", []RewriteRule{{Targets: []RewriteTarget{{Model: "gone"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.dir.Decide(context.Background(), inbound(t, `{"messages":[]}`)); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("err %v, want ErrUnknownModel instead of the default pool", err)
	}
}

func TestCatchAllRuleAppliesWithoutModel(t *testing.T) {
	e := newEnv(t)
	e.store.PutPool(datalayer.Pool{ID: "p2", Model: "mistral"})
	e.store.UpsertEndpoint(endpoint("e9", "p2", "10.0.1.1", true))
	e.table.Put("p2", []string{"mistral"}, nil)
	if err := e.table.SetRules("s", []RewriteRule{{Targets: []RewriteTarget{{Model: "mistral", Weight: 1}}}}); err != nil {
		t.Fatal(err)
	}
	dec, err := e.dir.Decide(context.Background(), inbound(t, `{"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Pool.ID != "p2" || dec.TargetModel != "mistral" {
		t.Fatalf("pool %s target %s, want p2 mistral", dec.Pool.ID, dec.TargetModel)
	}
	e.dir.ResponseComplete(context.Background(), dec, nil)
	e.settled(t)
}

func TestObjectiveAndFairness(t *testing.T) {
	cases := []struct {
		name     string
		headers  []string
		identity string
		obj      framework.Objective
		fairness string
		err      bool
	}{
		{"defaults", nil, "", framework.Objective{}, "anonymous", false},
		{"identity", nil, "user-7", framework.Objective{}, "user-7", false},
		{"header over identity", []string{HeaderFairnessID, "tenant-a"}, "user-7", framework.Objective{}, "tenant-a", false},
		{"priority", []string{HeaderObjective, "-2"}, "", framework.Objective{Priority: -2}, "anonymous", false},
		{"latency targets", []string{HeaderObjective, " 5 ", HeaderTTFTObjective, "250", HeaderTPOTObjective, "40"}, "",
			framework.Objective{Priority: 5, TTFT: 250 * time.Millisecond, TPOT: 40 * time.Millisecond}, "anonymous", false},
		{"bad priority", []string{HeaderObjective, "high"}, "", framework.Objective{}, "", true},
		{"bad ttft", []string{HeaderTTFTObjective, "1.5"}, "", framework.Objective{}, "", true},
		{"negative tpot", []string{HeaderTPOTObjective, "-1"}, "", framework.Objective{}, "", true},
		{"overflowing ttft", []string{HeaderTTFTObjective, "9223372036854775807"}, "", framework.Objective{}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			in := inbound(t, chat("llama"), c.headers...)
			in.Identity = c.identity
			dec, err := e.dir.Decide(context.Background(), in)
			if c.err {
				if !errors.Is(err, ErrInvalidHeader) {
					t.Fatalf("err %v, want ErrInvalidHeader", err)
				}
				if code, _ := Status(err); code != http.StatusBadRequest {
					t.Fatalf("status %d, want 400", code)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if dec.Request.Objective != c.obj || dec.Request.FairnessID != c.fairness {
				t.Fatalf("objective %+v fairness %q, want %+v %q", dec.Request.Objective, dec.Request.FairnessID, c.obj, c.fairness)
			}
			item := e.flow.lastItem(t)
			want := flowcontrol.FlowKey{FairnessID: c.fairness, Priority: c.obj.Priority}
			if item.Flow != want || item.Priority != c.obj.Priority {
				t.Fatalf("item %+v, want flow %+v and objective %+v", item, want, c.obj)
			}
			e.dir.ResponseComplete(context.Background(), dec, nil)
		})
	}
}

func TestRequestFromBody(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		prompt   string
		tokens   int
		expected int
		stream   bool
	}{
		{"defaults", chat("llama"), "hello world", 3, defaultOutput, false},
		{"bounded stream", `{"model":"llama","stream":true,"max_completion_tokens":50,"messages":[{"role":"user","content":"abcd"}]}`, "abcd", 1, 50, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			dec, err := e.dir.Decide(context.Background(), inbound(t, c.body, HeaderRequestID, "rid-1"))
			if err != nil {
				t.Fatal(err)
			}
			r := dec.Request
			if r.Prompt != c.prompt || r.PromptTokens != c.tokens || r.ExpectedOutputTokens != c.expected || r.Streaming != c.stream {
				t.Fatalf("request %+v", r)
			}
			if item := e.flow.lastItem(t); item.Cost != c.tokens+c.expected || item.Pool != "p1" || item.ID != "rid-1" {
				t.Fatalf("item %+v", item)
			}
			if r.ID != "rid-1" || dec.Headers[HeaderRequestID] != "rid-1" || r.Headers[HeaderRequestID] != "rid-1" {
				t.Fatalf("request id %q header %q", r.ID, dec.Headers[HeaderRequestID])
			}
			e.dir.ResponseComplete(context.Background(), dec, nil)
		})
	}
	e := newEnv(t)
	a, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if err != nil {
		t.Fatal(err)
	}
	if a.Request.ID == "" || a.Request.ID == b.Request.ID {
		t.Fatalf("generated ids %q and %q", a.Request.ID, b.Request.ID)
	}
}

func TestSubset(t *testing.T) {
	cases := []struct {
		name    string
		headers []string
		set     bool
		subset  []string
		picked  []string
		err     error
	}{
		{"absent", nil, false, nil, []string{"e1", "e2"}, nil},
		{"empty header", []string{HeaderSubset, ""}, true, nil, nil, ErrNoReadyEndpoints},
		{"one", []string{HeaderSubset, "10.0.0.2:8000"}, true, []string{"10.0.0.2:8000"}, []string{"e2"}, nil},
		{"list with spaces", []string{HeaderSubset, " 10.0.0.2:8000 , 10.0.0.1:8000,"}, true, []string{"10.0.0.2:8000", "10.0.0.1:8000"}, []string{"e1", "e2"}, nil},
		{"repeated header", []string{HeaderSubset, "10.0.0.1:8000", HeaderSubset, "[::1]:9"}, true, []string{"10.0.0.1:8000", "[::1]:9"}, []string{"e1"}, nil},
		{"outside the pool", []string{HeaderSubset, "10.9.9.9:1"}, true, []string{"10.9.9.9:1"}, nil, ErrNoReadyEndpoints},
		{"malformed", []string{HeaderSubset, "10.0.0.1"}, true, nil, nil, ErrInvalidHeader},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			var seen *framework.Request
			e.sched.fn = func(ctx context.Context, req *framework.Request, cands []datalayer.Endpoint) (*framework.Result, error) {
				seen = req
				return pickAll(ctx, req, cands)
			}
			dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama"), c.headers...))
			if seen != nil {
				var got []string
				for _, a := range seen.Subset {
					got = append(got, a.String())
				}
				if seen.SubsetSet != c.set || !slices.Equal(got, c.subset) {
					t.Fatalf("subset %v set %v, want %v %v", got, seen.SubsetSet, c.subset, c.set)
				}
			}
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("err %v, want %v", err, c.err)
				}
				e.settled(t)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, ep := range dec.Endpoints {
				ids = append(ids, string(ep.ID()))
			}
			if !slices.Equal(ids, c.picked) {
				t.Fatalf("picked %v, want %v", ids, c.picked)
			}
			e.dir.ResponseComplete(context.Background(), dec, nil)
			e.settled(t)
		})
	}
}

func TestFailureModes(t *testing.T) {
	boom := errors.New("scheduler down")
	cases := []struct {
		name    string
		mode    datalayer.FailureMode
		config  string
		sched   func(context.Context, *framework.Request, []datalayer.Endpoint) (*framework.Result, error)
		headers []string
		picked  string
		err     error
		status  int
	}{
		{"fail close", datalayer.FailClose, "", func(context.Context, *framework.Request, []datalayer.Endpoint) (*framework.Result, error) {
			return nil, boom
		}, nil, "", ErrSchedulerUnavailable, 503},
		{"empty mode closes", "", "", func(context.Context, *framework.Request, []datalayer.Endpoint) (*framework.Result, error) {
			return nil, boom
		}, nil, "", ErrSchedulerUnavailable, 503},
		{"fail open least loaded", datalayer.FailOpen, "", func(context.Context, *framework.Request, []datalayer.Endpoint) (*framework.Result, error) {
			return nil, boom
		}, nil, "e3", nil, 200},
		{"fail open inside subset", datalayer.FailOpen, "", func(context.Context, *framework.Request, []datalayer.Endpoint) (*framework.Result, error) {
			return nil, boom
		}, []string{HeaderSubset, "10.0.0.1:8000"}, "e1", nil, 200},
		{"fail open with nothing ready", datalayer.FailOpen, "", func(context.Context, *framework.Request, []datalayer.Endpoint) (*framework.Result, error) {
			return nil, boom
		}, []string{HeaderSubset, "10.0.0.4:8000"}, "", ErrNoReadyEndpoints, 503},
		{"panic fails open", datalayer.FailOpen, "", func(context.Context, *framework.Request, []datalayer.Endpoint) (*framework.Result, error) {
			panic("bad plugin")
		}, nil, "e3", nil, 200},
		{"unknown scheduler config closes", datalayer.FailClose, "missing", nil, nil, "", ErrSchedulerUnavailable, 503},
		{"unknown scheduler config closes a fail open pool", datalayer.FailOpen, "missing", nil, nil, "", ErrUnknownSchedulerConfig, 503},
		{"no eligible endpoint is not a scheduler failure", datalayer.FailOpen, "", func(context.Context, *framework.Request, []datalayer.Endpoint) (*framework.Result, error) {
			return nil, fmt.Errorf("filter: %w", framework.ErrNoEndpoints)
		}, nil, "", ErrNoReadyEndpoints, 503},
		{"empty result", datalayer.FailOpen, "", func(context.Context, *framework.Request, []datalayer.Endpoint) (*framework.Result, error) {
			return &framework.Result{}, nil
		}, nil, "", ErrNoReadyEndpoints, 503},
		{"scheduler shed", datalayer.FailOpen, "", func(context.Context, *framework.Request, []datalayer.Endpoint) (*framework.Result, error) {
			return nil, fmt.Errorf("soft saturation: %w", flowcontrol.ErrShed)
		}, nil, "", ErrShed, 429},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.store.PutPool(datalayer.Pool{ID: "p1", Model: "llama", FailureMode: c.mode, SchedulerConfig: c.config})
			e.store.UpsertEndpoint(endpoint("e3", "p1", "10.0.0.3", true))
			e.store.UpsertEndpoint(endpoint("e4", "p1", "10.0.0.4", false))
			e.sched.fn = c.sched
			e.dir.loads.add("e1", 2)
			e.dir.loads.add("e2", 1)
			dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama"), c.headers...))
			if code, _ := Status(err); code != c.status {
				t.Fatalf("status %d for %v, want %d", code, err, c.status)
			}
			e.dir.loads.add("e1", -2)
			e.dir.loads.add("e2", -1)
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("err %v, want %v", err, c.err)
				}
				e.settled(t)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !dec.FailedOpen || dec.Endpoints[0].ID() != datalayer.EndpointID(c.picked) || dec.Result.PrimaryProfile != FailOpenProfile {
				t.Fatalf("failed open %v primary %s, want %s", dec.FailedOpen, dec.Endpoints[0].ID(), c.picked)
			}
			for _, ep := range dec.Endpoints {
				if !ep.Ready() {
					t.Fatalf("fail open offered unready %s", ep.ID())
				}
			}
			e.dir.ResponseComplete(context.Background(), dec, nil)
			e.settled(t)
		})
	}
}

func TestNamedSchedulerConfig(t *testing.T) {
	named := &fakeScheduler{}
	e := newEnv(t, func(d *Deps) { d.Schedulers = map[string]framework.Scheduler{"fast": named} })
	e.store.PutPool(datalayer.Pool{ID: "p1", Model: "llama", SchedulerConfig: "fast"})
	dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if err != nil {
		t.Fatal(err)
	}
	if named.calls.Load() != 1 || e.sched.calls.Load() != 0 {
		t.Fatalf("named scheduler ran %d times, default %d", named.calls.Load(), e.sched.calls.Load())
	}
	e.dir.ResponseComplete(context.Background(), dec, nil)
}

func TestAdmissionRefusals(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		want   error
		status int
	}{
		{"shed", flowcontrol.ErrShed, ErrShed, 429},
		{"queue full", flowcontrol.ErrQueueFull, ErrShed, 429},
		{"queue timeout", flowcontrol.ErrQueueTimeout, ErrShed, 429},
		{"canceled", context.Canceled, context.Canceled, StatusClientClosed},
		{"deadline", context.DeadlineExceeded, context.DeadlineExceeded, 504},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.flow.err = c.err
			_, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
			if !errors.Is(err, c.want) {
				t.Fatalf("err %v, want %v", err, c.want)
			}
			if code, retry := Status(err); code != c.status || (code == 429) != (retry != "") {
				t.Fatalf("status %d retry %q, want %d", code, retry, c.status)
			}
			if e.sched.calls.Load() != 0 {
				t.Fatal("a refused request was scheduled")
			}
			if e.observer.refused.Load() != 1 || e.observer.decided.Load() != 0 {
				t.Fatal("the observer missed the refusal")
			}
			e.settled(t)
		})
	}
}

func TestEmptyPoolAnswersNoReadyEndpoints(t *testing.T) {
	e := newEnv(t)
	e.store.DeleteEndpoint("e1")
	e.store.DeleteEndpoint("e2")
	_, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if code, retry := Status(err); !errors.Is(err, ErrNoReadyEndpoints) || code != 503 || retry == "" {
		t.Fatalf("err %v status %d retry %q", err, code, retry)
	}
	e.settled(t)
}

func TestDecisionHeaders(t *testing.T) {
	e := newEnv(t)
	dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama"), HeaderRequestID, "r1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := dec.Headers[HeaderDestination]; got != "10.0.0.1:8000,10.0.0.2:8000" {
		t.Fatalf("destination %q", got)
	}
	if dec.Pool.ID != "p1" || dec.Permit == nil || dec.Result == nil || dec.FailedOpen {
		t.Fatalf("decision %+v", dec)
	}
	e.dir.ResponseComplete(context.Background(), dec, nil)
	e.settled(t)
}

// Builds a director whose every stage records into rec
func hookedEnv(t *testing.T, rec *recorder, postErr error) *env {
	t.Helper()
	pre := &prePlugin{name: "p", fn: func(context.Context, *Inbound, *framework.Request) error { rec.add("rc.pre"); return nil }}
	post := &postPlugin{name: "p", fn: func(context.Context, *Inbound, *Decision) error { rec.add("rc.post"); return postErr }}
	hdr := &headersPlugin{name: "h", fn: func(_ context.Context, _ *Decision, _ *framework.Response, h http.Header) error {
		rec.add("rc.headers:" + h.Get(HeaderServed))
		return nil
	}}
	body := &bodyPlugin{name: "b", fn: func(chunk []byte, end bool) ([]byte, error) {
		rec.add(fmt.Sprintf("rc.body:%s:%v", chunk, end))
		return []byte(strings.ToUpper(string(chunk))), nil
	}}
	done := &donePlugin{name: "d", rec: rec}
	return newEnv(t,
		withTopology(t, Registration{Plugin: pre}, Registration{Plugin: post}, Registration{Plugin: hdr}, Registration{Plugin: body}, Registration{Plugin: done}),
		func(d *Deps) {
			d.Lifecycle = []framework.Plugin{&lifeHook{name: "a", rec: rec}, &lifeHook{name: "b", rec: rec}}
			d.Observer = nil
		})
}

func TestHookOrdering(t *testing.T) {
	rec := &recorder{}
	e := hookedEnv(t, rec, nil)
	ctx := context.Background()
	dec, err := e.dir.Decide(ctx, inbound(t, chat("llama")))
	if err != nil {
		t.Fatal(err)
	}
	resp := &framework.Response{Endpoint: dec.Endpoints[1]}
	headers := http.Header{}
	if err := e.dir.ResponseReceived(ctx, dec, resp, headers); err != nil {
		t.Fatal(err)
	}
	if headers.Get(HeaderServed) != "10.0.0.2:8000" || resp.Headers[HeaderServed] != "10.0.0.2:8000" {
		t.Fatalf("served header %q", headers.Get(HeaderServed))
	}
	out, err := e.dir.ResponseChunk(ctx, dec, resp, []byte("tok"), false)
	if err != nil || string(out) != "TOK" {
		t.Fatalf("chunk %q %v", out, err)
	}
	if out, err = e.dir.ResponseChunk(ctx, dec, resp, []byte("end"), true); err != nil || string(out) != "END" {
		t.Fatalf("chunk %q %v", out, err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { e.dir.ResponseComplete(ctx, dec, resp) })
	}
	wg.Wait()
	want := []string{
		"rc.pre", "a.pre", "b.pre", "rc.post",
		"a.received", "b.received", "rc.headers:10.0.0.2:8000",
		"a.chunk:tok", "b.chunk:tok", "rc.body:tok:false",
		"a.chunk:end", "b.chunk:end", "rc.body:end:true",
		"a.complete", "b.complete", "d.done",
	}
	if got := rec.list(); !slices.Equal(got, want) {
		t.Fatalf("events\n got %v\nwant %v", got, want)
	}
	e.settled(t)
}

func TestReceivedDefaultsToPrimary(t *testing.T) {
	e := newEnv(t)
	dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if err != nil {
		t.Fatal(err)
	}
	resp := &framework.Response{}
	h := http.Header{}
	if err := e.dir.ResponseReceived(context.Background(), dec, resp, h); err != nil {
		t.Fatal(err)
	}
	if resp.Endpoint.ID() != "e1" || h.Get(HeaderServed) != "10.0.0.1:8000" {
		t.Fatalf("served %s header %q", resp.Endpoint.ID(), h.Get(HeaderServed))
	}
	if err := e.dir.ResponseReceived(context.Background(), dec, resp, nil); err == nil {
		t.Fatal("nil headers accepted")
	}
	e.dir.ResponseComplete(context.Background(), dec, resp)
	e.settled(t)
}

func TestServedEndpointCarriesTheLoad(t *testing.T) {
	e := newEnv(t)
	dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if err != nil {
		t.Fatal(err)
	}
	if e.dir.loads.get("e1") != 1 {
		t.Fatal("primary not counted")
	}
	if err := e.dir.ResponseReceived(context.Background(), dec, &framework.Response{Endpoint: dec.Endpoints[1]}, http.Header{}); err != nil {
		t.Fatal(err)
	}
	if e.dir.loads.get("e1") != 0 || e.dir.loads.get("e2") != 1 {
		t.Fatal("load did not move to the serving endpoint")
	}
	e.dir.ResponseComplete(context.Background(), dec, nil)
	if err := e.dir.ResponseReceived(context.Background(), dec, &framework.Response{Endpoint: dec.Endpoints[0]}, http.Header{}); err != nil {
		t.Fatal(err)
	}
	e.settled(t)
}

func TestPostDecisionErrorCompletesOnce(t *testing.T) {
	rec := &recorder{}
	boom := errors.New("post failed")
	e := hookedEnv(t, rec, boom)
	_, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if !errors.Is(err, boom) {
		t.Fatalf("err %v, want post failure", err)
	}
	if code, _ := Status(err); code != 500 {
		t.Fatalf("status %d, want 500", code)
	}
	for _, ev := range []string{"a.pre", "a.complete", "b.complete", "d.done"} {
		if rec.count(ev) != 1 {
			t.Fatalf("%s ran %d times in %v", ev, rec.count(ev), rec.list())
		}
	}
	e.settled(t)
}

func TestPreRequestErrorAbortsBeforeAdmission(t *testing.T) {
	boom := errors.New("tokenizer down")
	var later bool
	first := &prePlugin{name: "first", fn: func(context.Context, *Inbound, *framework.Request) error { return boom }}
	slow := &prePlugin{name: "slow", fn: func(ctx context.Context, _ *Inbound, _ *framework.Request) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return errors.New("not cancelled")
		}
	}}
	after := &prePlugin{name: "after", fn: func(context.Context, *Inbound, *framework.Request) error { later = true; return nil }}
	e := newEnv(t, withTopology(t,
		Registration{Plugin: first},
		Registration{Plugin: slow},
		Registration{Plugin: after, After: []framework.TypedName{first.TypedName(), slow.TypedName()}},
	))
	start := time.Now()
	_, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if !errors.Is(err, boom) {
		t.Fatalf("err %v, want the first error", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the batch was not cancelled on the first error")
	}
	if later || e.flow.admitted.Load() != 0 {
		t.Fatal("a later batch or admission ran after the error")
	}
}

func TestPreRequestBatchRunsConcurrentlyAndMerges(t *testing.T) {
	var arrived sync.WaitGroup
	arrived.Add(2)
	meet := func() error {
		arrived.Done()
		done := make(chan struct{})
		go func() { arrived.Wait(); close(done) }()
		select {
		case <-done:
			return nil
		case <-time.After(5 * time.Second):
			return errors.New("plugins of one batch did not run together")
		}
	}
	a := &prePlugin{name: "a", fn: func(_ context.Context, _ *Inbound, r *framework.Request) error {
		if err := meet(); err != nil {
			return err
		}
		r.Objective.Priority = 9
		r.Headers["x-a"] = "1"
		delete(r.Headers, "X-Drop")
		return nil
	}}
	b := &prePlugin{name: "b", fn: func(_ context.Context, _ *Inbound, r *framework.Request) error {
		if err := meet(); err != nil {
			return err
		}
		r.ExpectedOutputTokens = 7
		r.Headers["x-b"] = "2"
		r.Subset = []datalayer.Address{{Host: "10.0.0.2", Port: 8000}}
		r.SubsetSet = true
		return nil
	}}
	c := &prePlugin{name: "c", fn: func(_ context.Context, _ *Inbound, r *framework.Request) error {
		if r.Objective.Priority != 9 || r.ExpectedOutputTokens != 7 {
			return fmt.Errorf("the next batch saw %+v", r)
		}
		return nil
	}}
	e := newEnv(t, withTopology(t, Registration{Plugin: a}, Registration{Plugin: b}, Registration{Plugin: c, After: []framework.TypedName{a.TypedName(), b.TypedName()}}))
	dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama"), "X-Drop", "y"))
	if err != nil {
		t.Fatal(err)
	}
	r := dec.Request
	if r.Objective.Priority != 9 || r.ExpectedOutputTokens != 7 || r.Headers["x-a"] != "1" || r.Headers["x-b"] != "2" || r.Headers["X-Drop"] != "" {
		t.Fatalf("merged request %+v", r)
	}
	if item := e.flow.lastItem(t); item.Priority != 9 || item.Cost != r.PromptTokens+7 {
		t.Fatalf("flow item %+v ignores plugin changes", item)
	}
	if len(dec.Endpoints) != 1 || dec.Endpoints[0].ID() != "e2" {
		t.Fatalf("subset set by a plugin was not honored, picked %v", dec.Endpoints)
	}
	e.dir.ResponseComplete(context.Background(), dec, nil)
}

func TestResponseHeadersBatchMerges(t *testing.T) {
	a := &headersPlugin{name: "a", fn: func(_ context.Context, _ *Decision, _ *framework.Response, h http.Header) error {
		h.Set("X-A", "1")
		h.Del("X-Remove")
		return nil
	}}
	b := &headersPlugin{name: "b", fn: func(_ context.Context, _ *Decision, _ *framework.Response, h http.Header) error {
		h.Set("X-B", "2")
		return nil
	}}
	e := newEnv(t, withTopology(t, Registration{Plugin: a}, Registration{Plugin: b}))
	dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{"X-Remove": {"z"}, "X-Keep": {"k"}}
	if err := e.dir.ResponseReceived(context.Background(), dec, &framework.Response{}, h); err != nil {
		t.Fatal(err)
	}
	if h.Get("X-A") != "1" || h.Get("X-B") != "2" || h.Get("X-Keep") != "k" || h.Get("X-Remove") != "" || h.Get(HeaderServed) == "" {
		t.Fatalf("merged headers %v", h)
	}
	e.dir.ResponseComplete(context.Background(), dec, nil)
}

func TestPluginErrorsSurface(t *testing.T) {
	boom := errors.New("bad")
	hdr := &headersPlugin{name: "h", fn: func(context.Context, *Decision, *framework.Response, http.Header) error { return boom }}
	body := &bodyPlugin{name: "b", fn: func([]byte, bool) ([]byte, error) { panic("body plugin panic") }}
	e := newEnv(t, withTopology(t, Registration{Plugin: hdr}, Registration{Plugin: body}))
	dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.dir.ResponseReceived(context.Background(), dec, &framework.Response{}, http.Header{}); !errors.Is(err, boom) {
		t.Fatalf("headers err %v", err)
	}
	if _, err := e.dir.ResponseChunk(context.Background(), dec, &framework.Response{}, []byte("x"), true); err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("body err %v", err)
	}
	e.dir.ResponseComplete(context.Background(), dec, &framework.Response{Err: err})
	e.settled(t)
}

func TestConcurrentDecide(t *testing.T) {
	rec := &recorder{}
	e := hookedEnv(t, rec, nil)
	e.store.PutPool(datalayer.Pool{ID: "p1", Model: "llama", FailureMode: datalayer.FailOpen})
	if err := e.table.SetRules("split", []RewriteRule{{Match: "public", Targets: []RewriteTarget{{Model: "llama", Weight: 1}, {Model: "llama-lora", Weight: 1}}}}); err != nil {
		t.Fatal(err)
	}
	var fail sync.Mutex
	flaky := 0
	e.sched.fn = func(ctx context.Context, req *framework.Request, c []datalayer.Endpoint) (*framework.Result, error) {
		fail.Lock()
		flaky++
		odd := flaky%3 == 0
		fail.Unlock()
		if odd {
			return nil, errors.New("flaky")
		}
		return pickAll(ctx, req, c)
	}
	const n = 200
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			ctx := context.Background()
			headers := []string{HeaderFairnessID, fmt.Sprintf("t%d", i%5)}
			if i%7 == 0 {
				headers = append(headers, HeaderSubset, "10.0.0.2:8000")
			}
			dec, err := e.dir.Decide(ctx, inbound(t, chat("public"), headers...))
			if err != nil {
				t.Error(err)
				return
			}
			resp := &framework.Response{}
			if err := e.dir.ResponseReceived(ctx, dec, resp, http.Header{}); err != nil {
				t.Error(err)
			}
			if _, err := e.dir.ResponseChunk(ctx, dec, resp, []byte("x"), true); err != nil {
				t.Error(err)
			}
			e.dir.ResponseComplete(ctx, dec, resp)
			e.dir.ResponseComplete(ctx, dec, resp)
		})
	}
	wg.Wait()
	if e.flow.admitted.Load() != n || rec.count("a.complete") != n || rec.count("d.done") != n || rec.count("a.pre") != n {
		t.Fatalf("admitted %d completes %d done %d pre %d", e.flow.admitted.Load(), rec.count("a.complete"), rec.count("d.done"), rec.count("a.pre"))
	}
	e.settled(t)
}

func TestObserver(t *testing.T) {
	e := newEnv(t)
	dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if err != nil {
		t.Fatal(err)
	}
	e.dir.ResponseComplete(context.Background(), dec, nil)
	e.dir.ResponseComplete(context.Background(), dec, nil)
	if _, err := e.dir.Decide(context.Background(), inbound(t, chat("gpt"))); err == nil {
		t.Fatal("unknown model routed")
	}
	o := e.observer
	if o.decided.Load() != 1 || o.refused.Load() != 1 || o.completed.Load() != 1 {
		t.Fatalf("decided %d refused %d completed %d", o.decided.Load(), o.refused.Load(), o.completed.Load())
	}
}

func TestNewDirectorValidation(t *testing.T) {
	store := newFakeStore()
	full := func() Deps {
		return Deps{Store: store, Resolver: NewRuleTable(store), Sampler: idleSampler{}, Scheduler: &fakeScheduler{}, Flow: &fakeFlow{}, DefaultOutputTokens: 1}
	}
	cases := []struct {
		name string
		edit func(*Deps)
		ok   bool
	}{
		{"complete", func(*Deps) {}, true},
		{"named schedulers only", func(d *Deps) {
			d.Scheduler = nil
			d.Schedulers = map[string]framework.Scheduler{"x": &fakeScheduler{}}
		}, true},
		{"no store", func(d *Deps) { d.Store = nil }, false},
		{"no resolver", func(d *Deps) { d.Resolver = nil }, false},
		{"no flow", func(d *Deps) { d.Flow = nil }, false},
		{"no sampler", func(d *Deps) { d.Sampler = nil }, false},
		{"no scheduler", func(d *Deps) { d.Scheduler = nil }, false},
		{"no default output", func(d *Deps) { d.DefaultOutputTokens = 0 }, false},
		{"hookless lifecycle plugin", func(d *Deps) { d.Lifecycle = []framework.Plugin{&bothPlugin{name: "x"}} }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deps := full()
			c.edit(&deps)
			if _, err := NewDirector(deps); (err == nil) != c.ok {
				t.Fatalf("err %v, want ok %v", err, c.ok)
			}
		})
	}
}

func TestStatus(t *testing.T) {
	cases := []struct {
		err   error
		code  int
		retry bool
	}{
		{nil, 200, false},
		{fmt.Errorf("x: %w", ErrShed), 429, true},
		{ErrNoReadyEndpoints, 503, true},
		{ErrSchedulerUnavailable, 503, true},
		{ErrUnknownModel, 404, false},
		{ErrNoModel, 400, false},
		{ErrInvalidHeader, 400, false},
		{context.DeadlineExceeded, 504, false},
		{context.Canceled, StatusClientClosed, false},
		{errors.New("other"), 500, false},
	}
	for _, c := range cases {
		code, retry := Status(c.err)
		if code != c.code || (retry != "") != c.retry {
			t.Fatalf("Status(%v) = %d %q, want %d retry %v", c.err, code, retry, c.code, c.retry)
		}
	}
}

// The target model replaces the value bytes alone, every other byte of the body kept in order
func TestMutateRewritesModelInPlace(t *testing.T) {
	cases := []struct {
		name, body, target, want string
	}{
		{"spacing and key order", "{ \"messages\" : [ {\"role\":\"user\",\"content\":\"hi\"} ],\n\t\"model\":  \"llama\" , \"temperature\": 1.50, \"z\": 1e3 }", "llama-lora",
			"{ \"messages\" : [ {\"role\":\"user\",\"content\":\"hi\"} ],\n\t\"model\":  \"llama-lora\" , \"temperature\": 1.50, \"z\": 1e3 }"},
		{"model first and nested model kept", `{"model":"llama","metadata":{"model":"keep"},"messages":[]}`, "llama-lora",
			`{"model":"llama-lora","metadata":{"model":"keep"},"messages":[]}`},
		{"escapes and unicode kept", `{"messages":[{"role":"user","content":"héllo \"q\""}],"model":"llama","n":-0.0}`, "llama-lora",
			`{"messages":[{"role":"user","content":"héllo \"q\""}],"model":"llama-lora","n":-0.0}`},
		{"absent model appended after the last member", "{\"messages\":[] ,\n \"max_tokens\":8 }", "llama",
			"{\"messages\":[] ,\n \"max_tokens\":8,\"model\":\"llama\" }"},
		{"already the target", `{ "model" : "llama" }`, "llama", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := inbound(t, c.body).Body
			out, err := mutate(doc, c.target)
			if err != nil {
				t.Fatal(err)
			}
			if c.want == "" {
				if out != doc {
					t.Fatalf("a body naming the target was copied to %s", out.Bytes())
				}
				return
			}
			if string(out.Bytes()) != c.want {
				t.Fatalf("mutated\n%s\nwant\n%s", out.Bytes(), c.want)
			}
			if string(doc.Bytes()) != c.body {
				t.Fatalf("the inbound body changed to %s", doc.Bytes())
			}
		})
	}
	e := newEnv(t)
	body := "{\"stream\" : false,\n  \"model\":\"llama\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
	dec, err := e.dir.Decide(context.Background(), inbound(t, body, HeaderModelRewrite, "llama-lora"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\"stream\" : false,\n  \"model\":\"llama-lora\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"; string(dec.Body.Bytes()) != want {
		t.Fatalf("decided body %s, want %s", dec.Body.Bytes(), want)
	}
	e.dir.ResponseComplete(context.Background(), dec, nil)
	e.settled(t)
}

func TestSchedulerFallbackFailsOpen(t *testing.T) {
	cases := []struct {
		name       string
		mode       datalayer.FailureMode
		fallback   bool
		wantFlag   bool
		wantFailed bool
	}{
		{"fail open pool with fallback answer", datalayer.FailOpen, true, true, true},
		{"fail open pool with scheduler answer", datalayer.FailOpen, false, true, false},
		{"fail close pool never fails open", datalayer.FailClose, true, false, false},
		{"default mode never fails open", "", true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.store.PutPool(datalayer.Pool{ID: "p1", Model: "llama", FailureMode: c.mode})
			var seen bool
			e.sched.fn = func(ctx context.Context, req *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error) {
				seen = req.FailOpen
				res, err := pickAll(ctx, req, candidates)
				if err == nil {
					res.FailedOpen = c.fallback
				}
				return res, err
			}
			dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
			if err != nil {
				t.Fatal(err)
			}
			if seen != c.wantFlag || dec.FailedOpen != c.wantFailed {
				t.Fatalf("scheduler saw fail open %v, decision failed open %v", seen, dec.FailedOpen)
			}
			e.dir.ResponseComplete(context.Background(), dec, nil)
			e.settled(t)
		})
	}
}
