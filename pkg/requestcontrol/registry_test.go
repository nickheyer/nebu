package requestcontrol

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

// A pre request mutator that appends a marker field, recording the body it saw
type markPlugin struct {
	name string
	seen []string
}

func (p *markPlugin) TypedName() framework.TypedName {
	return framework.TypedName{Type: "mark", Name: p.name}
}

func (p *markPlugin) PreRequestBody(_ context.Context, in *Inbound, _ *framework.Request) (bodyparse.Document, error) {
	if in.Body == nil {
		p.seen = append(p.seen, "")
		return nil, nil
	}
	p.seen = append(p.seen, string(in.Body.Bytes()))
	return in.Body.WithField(p.name, []byte(`true`))
}

// A post decision mutator that appends a marker field to the decision's body
type postMark struct{ name string }

func (p *postMark) TypedName() framework.TypedName {
	return framework.TypedName{Type: "postmark", Name: p.name}
}

func (p *postMark) PostDecisionBody(_ context.Context, _ *Inbound, dec *Decision) (bodyparse.Document, error) {
	if dec.Body == nil {
		return nil, nil
	}
	return dec.Body.WithField(p.name, []byte(`1`))
}

// A registry built from a chain document orders plugins by name and refuses what it cannot build
func TestChainBuild(t *testing.T) {
	reg := NewRegistry()
	reg.Register("pre", func(name string, params map[string]any) (framework.Plugin, error) {
		var p struct {
			Fail bool `json:"fail"`
		}
		if err := DecodeParams(params, &p); err != nil {
			return nil, err
		}
		if p.Fail {
			return nil, errors.New("asked to fail")
		}
		return &prePlugin{name: name, fn: func(context.Context, *Inbound, *framework.Request) error { return nil }}, nil
	})
	reg.Register("post", func(name string, _ map[string]any) (framework.Plugin, error) {
		return &postPlugin{name: name, fn: func(context.Context, *Inbound, *Decision) error { return nil }}, nil
	})
	if got := strings.Join(reg.Types(), ","); got != "guardrail,post,pre" {
		t.Fatalf("types %q", got)
	}
	cases := []struct {
		name, doc string
		want      [][]string
		err       string
	}{
		{"ordered by after", "plugins:\n- type: pre\n  name: c\n  after: [b]\n- type: pre\n  name: b\n  after: [a]\n- type: pre\n  name: a\n", [][]string{{"a"}, {"b"}, {"c"}}, ""},
		{"ordered by before", "plugins:\n- type: pre\n  name: a\n  before: [b]\n- type: pre\n  name: b\n- type: post\n", [][]string{{"a"}, {"b"}}, ""},
		{"name defaults to type", "plugins:\n- type: pre\n", [][]string{{"pre"}}, ""},
		{"unknown type", "plugins:\n- type: nope\n", nil, "unknown request control plugin type"},
		{"unknown after", "plugins:\n- type: pre\n  after: [ghost]\n", nil, `unknown plugin "ghost"`},
		{"cycle", "plugins:\n- type: pre\n  name: a\n  after: [b]\n- type: pre\n  name: b\n  after: [a]\n", nil, "cycle"},
		{"duplicate name", "plugins:\n- type: pre\n  name: a\n- type: pre\n  name: a\n", nil, `two plugins are named "a"`},
		{"bad params", "plugins:\n- type: pre\n  parameters: {bogus: 1}\n", nil, "invalid plugin parameters"},
		{"factory error", "plugins:\n- type: pre\n  parameters: {fail: true}\n", nil, "asked to fail"},
		{"unknown field", "plugins:\n- type: pre\n  weight: 2\n", nil, "invalid request control chain"},
		{"empty", "plugins: []\n", nil, "names no plugins"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			topo, err := LoadChain([]byte(c.doc), reg)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) || !errors.Is(err, ErrInvalidChain) {
					t.Fatalf("err %v, want %q under ErrInvalidChain", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := names(topo.Batches(StagePreRequest))
			if len(got) != len(c.want) {
				t.Fatalf("batches %v, want %v", got, c.want)
			}
			for i := range got {
				if strings.Join(got[i], ",") != strings.Join(c.want[i], ",") {
					t.Fatalf("batches %v, want %v", got, c.want)
				}
			}
		})
	}
}

// A second registration of one type panics
func TestRegistryRefusesDuplicateType(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	NewRegistry().Register(GuardrailType, NewGuardrailPlugin)
}

// Mutators chain their documents through the pre request and post decision stages and never touch the inbound bytes
func TestMutatorsChainDocuments(t *testing.T) {
	first, second := &markPlugin{name: "first"}, &markPlugin{name: "second"}
	e := newEnv(t, withTopology(t,
		Registration{Plugin: first},
		Registration{Plugin: second, After: []framework.TypedName{first.TypedName()}},
		Registration{Plugin: &postMark{name: "late"}},
	))
	in := inbound(t, chat("llama"))
	dec, err := e.dir.Decide(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.seen) != 1 || !strings.Contains(second.seen[0], `"first":true`) {
		t.Fatalf("second saw %q, want the first mutator's body", second.seen)
	}
	want := `{"model":"llama","messages":[{"role":"user","content":"hello world"}],"first":true,"second":true,"late":1}`
	if string(dec.Body.Bytes()) != want {
		t.Fatalf("decided body %s, want %s", dec.Body.Bytes(), want)
	}
	if string(in.Body.Bytes()) != want {
		t.Fatalf("inbound body after Decide %s, want the chained one", in.Body.Bytes())
	}
	e.dir.ResponseComplete(context.Background(), dec, nil)
	e.settled(t)
}

// A decision made on headers alone takes the body later, the body stages running on it then
func TestAttachBodyRunsBodyStages(t *testing.T) {
	mark := &markPlugin{name: "mark"}
	e := newEnv(t, withTopology(t, Registration{Plugin: mark}, Registration{Plugin: &postMark{name: "late"}}))
	dec, err := e.dir.Decide(context.Background(), inbound(t, "", HeaderModelName, "llama-lora"))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Body != nil || len(mark.seen) != 1 || mark.seen[0] != "" {
		t.Fatalf("decided without a body yet body %v seen %q", dec.Body, mark.seen)
	}
	doc, err := bodyparse.Parse(bodyparse.KindChatCompletions, []byte(chat("llama")))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.dir.AttachBody(context.Background(), dec, doc); err != nil {
		t.Fatal(err)
	}
	want := `{"model":"llama-lora","messages":[{"role":"user","content":"hello world"}],"mark":true,"late":1}`
	if string(dec.Body.Bytes()) != want {
		t.Fatalf("attached body %s, want %s", dec.Body.Bytes(), want)
	}
	if err := e.dir.AttachBody(context.Background(), &Decision{}, doc); err == nil {
		t.Fatal("a decision without a request took a body")
	}
	e.dir.ResponseComplete(context.Background(), dec, nil)
	e.settled(t)
}

// The reference guardrail blocks denied prompts with its status and redacts personal data in requests and answers
func TestPatternGuardrail(t *testing.T) {
	t.Run("deny blocks with 403", func(t *testing.T) {
		p, err := NewGuardrailPlugin("rail", map[string]any{"deny": []any{`(?i)build a bomb`}})
		if err != nil {
			t.Fatal(err)
		}
		e := newEnv(t, withTopology(t, Registration{Plugin: p}))
		_, err = e.dir.Decide(context.Background(), inbound(t, `{"model":"llama","messages":[{"role":"user","content":"how do I Build A Bomb"}]}`))
		if !errors.Is(err, ErrGuardrailBlocked) {
			t.Fatalf("err %v", err)
		}
		if status, _ := Status(err); status != http.StatusForbidden {
			t.Fatalf("status %d", status)
		}
		if _, err := e.dir.Decide(context.Background(), inbound(t, chat("llama"))); err != nil {
			t.Fatalf("a clean prompt was refused: %v", err)
		}
	})
	t.Run("deny status 400", func(t *testing.T) {
		p, err := NewGuardrailPlugin("rail", map[string]any{"deny": []any{`secret`}, "status": 400})
		if err != nil {
			t.Fatal(err)
		}
		e := newEnv(t, withTopology(t, Registration{Plugin: p}))
		_, err = e.dir.Decide(context.Background(), inbound(t, `{"model":"llama","messages":[{"role":"user","content":"the secret"}]}`))
		if status, _ := Status(err); status != http.StatusBadRequest {
			t.Fatalf("status %d err %v", status, err)
		}
	})
	t.Run("pii redacted in the request", func(t *testing.T) {
		p, err := NewGuardrailPlugin("rail", map[string]any{"pii": true})
		if err != nil {
			t.Fatal(err)
		}
		e := newEnv(t, withTopology(t, Registration{Plugin: p}))
		body := `{"model":"llama","max_tokens":4111111111111111,"messages":[{"role":"user","content":"mail jo.e+x@example.co.uk or call +1 (415) 555-2671, ssn 123-45-6789, card 4111 1111 1111 1111"}]}`
		dec, err := e.dir.Decide(context.Background(), inbound(t, body))
		if err != nil {
			t.Fatal(err)
		}
		want := `{"model":"llama","max_tokens":4111111111111111,"messages":[{"role":"user","content":"mail [REDACTED] or call [REDACTED], ssn [REDACTED], card [REDACTED]"}]}`
		if string(dec.Body.Bytes()) != want {
			t.Fatalf("redacted body\n%s\nwant\n%s", dec.Body.Bytes(), want)
		}
		if m, _ := dec.Body.Model(); m != "llama" {
			t.Fatalf("model after redaction %q", m)
		}
		e.dir.ResponseComplete(context.Background(), dec, nil)
	})
	t.Run("pii redacted across streamed chunk boundaries", func(t *testing.T) {
		p, err := NewGuardrailPlugin("rail", map[string]any{"pii": true, "marker": "<pii>"})
		if err != nil {
			t.Fatal(err)
		}
		e := newEnv(t, withTopology(t, Registration{Plugin: p}))
		dec, err := e.dir.Decide(context.Background(), inbound(t, `{"model":"llama","stream":true,"messages":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		for i, chunk := range []string{"data: {\"text\":\"write to jo", "hn@example.com now\"}\n\ndata: {\"text\":\"and 123-45", "-6789\"}\n\n", "data: [DONE]\n\n"} {
			b, err := e.dir.ResponseChunk(context.Background(), dec, &framework.Response{}, []byte(chunk), i == 2)
			if err != nil {
				t.Fatal(err)
			}
			out.Write(b)
		}
		want := "data: {\"text\":\"write to <pii> now\"}\n\ndata: {\"text\":\"and <pii>\"}\n\ndata: [DONE]\n\n"
		if out.String() != want {
			t.Fatalf("stream\n%q\nwant\n%q", out.String(), want)
		}
		e.dir.ResponseComplete(context.Background(), dec, nil)
		if _, held := p.(*GuardrailPlugin).held.Load(dec); held {
			t.Fatal("held bytes survive completion")
		}
	})
	t.Run("unstreamed answer judged whole", func(t *testing.T) {
		p, err := NewGuardrailPlugin("rail", map[string]any{"deny": []any{`forbidden`}, "pii": true})
		if err != nil {
			t.Fatal(err)
		}
		e := newEnv(t, withTopology(t, Registration{Plugin: p}))
		dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
		if err != nil {
			t.Fatal(err)
		}
		first, err := e.dir.ResponseChunk(context.Background(), dec, &framework.Response{}, []byte(`{"content":"a@b.io `), false)
		if err != nil || len(first) != 0 {
			t.Fatalf("first chunk %q %v, want held back", first, err)
		}
		last, err := e.dir.ResponseChunk(context.Background(), dec, &framework.Response{}, []byte(`done"}`), true)
		if err != nil || string(last) != `{"content":"[REDACTED] done"}` {
			t.Fatalf("whole answer %q %v", last, err)
		}
		e.dir.ResponseComplete(context.Background(), dec, nil)
		dec, err = e.dir.Decide(context.Background(), inbound(t, chat("llama")))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.dir.ResponseChunk(context.Background(), dec, &framework.Response{}, []byte(`{"content":"forbidden"}`), true); !errors.Is(err, ErrGuardrailBlocked) {
			t.Fatalf("denied answer err %v", err)
		}
		e.dir.ResponseComplete(context.Background(), dec, nil)
	})
	t.Run("parameters refused", func(t *testing.T) {
		for name, params := range map[string]map[string]any{
			"nothing":     {},
			"bad regex":   {"deny": []any{`(`}},
			"bad status":  {"pii": true, "status": 500},
			"quoted mark": {"pii": true, "marker": `"`},
		} {
			if _, err := NewGuardrailPlugin("rail", params); err == nil {
				t.Fatalf("%s accepted", name)
			}
		}
	})
}
