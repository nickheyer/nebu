package sources

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A provider that lists two models to the key it was given, Anthropic's way when anthropic is set
func modelServer(t *testing.T, key string, anthropic bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
			return
		}
		if !anthropic {
			io.WriteString(w, `{"object":"list","data":[{"id":"small","created":1700000000,"owned_by":"lab","context_window":8192},{"id":"org/large","created":1710000000}]}`)
			return
		}
		if r.URL.Query().Get("after_id") == "" {
			io.WriteString(w, `{"data":[{"id":"claude-a","display_name":"Claude A","created_at":"2026-01-02T00:00:00Z","max_input_tokens":200000}],"has_more":true,"last_id":"claude-a"}`)
			return
		}
		io.WriteString(w, `{"data":[{"id":"claude-b","display_name":"Claude B","created_at":"2026-03-04T00:00:00Z"}],"has_more":false}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A context carrying the key for the source as the caller's
func withKey(id, key string) context.Context {
	return WithAuthorizer(context.Background(), func(sourceID string) http.Header {
		if sourceID != id {
			return nil
		}
		return http.Header{"Authorization": {"Bearer " + key}}
	})
}

// The reference of a model holds the reasoning effort levels its provider lists for it, lowest first, with the provider's word on
// each and its default: ChatGPT's own listing, OpenRouter's, and Anthropic's, which names no default. A listing with another
// thing under those names says nothing, and a model the listing is silent on takes the levels its provider's API names
func TestReferenceHoldsListedEfforts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"models":[
			{"slug":"plan","display_name":"Plan","visibility":"list","context_window":400000,"default_reasoning_level":"medium","supported_reasoning_levels":[{"effort":"xhigh","description":"Extra depth"},{"effort":"low","description":"Quick work"},{"effort":"medium","description":"Balanced"},{"effort":"later","description":"Not named yet"}]},
			{"slug":"hidden","visibility":"hide","supported_reasoning_levels":[{"effort":"low","description":""}]}],
		"data":[
			{"id":"routed","context_length":128000,"reasoning":{"default_effort":"high","default_enabled":true,"mandatory":false,"supported_efforts":["high","medium","low","minimal"]}},
			{"id":"claude","max_input_tokens":200000,"capabilities":{"effort":{"supported":true,"low":{"supported":true},"medium":{"supported":true},"high":{"supported":true},"xhigh":null,"max":{"supported":false}},"thinking":{"supported":true}}},
			{"id":"other","reasoning":true,"capabilities":["chat"],"supported_reasoning_levels":"all","default_reasoning_level":3},
			{"id":"silent"}]}`)
	}))
	t.Cleanup(srv.Close)
	read := func(cat *Catalog, repo string) *Reference {
		t.Helper()
		c := testClient(t, cat, &v1.Source{Id: "lab", Kind: cat.Kind, Config: map[string]string{"endpoint": srv.URL + "/v1"}})
		m, err := c.Resolve(withKey("lab", "k"), repo, "")
		if err != nil {
			t.Fatal(err)
		}
		blob, err := c.Open(context.Background(), m, m.Artifacts[0])
		if err != nil {
			t.Fatal(err)
		}
		data := make([]byte, blob.Size())
		if _, err := blob.ReadAt(data, 0); err != nil && err != io.EOF {
			t.Fatal(err)
		}
		ref, err := ParseReference(data)
		if err != nil {
			t.Fatal(err)
		}
		return ref
	}
	levels := func(ref *Reference) string {
		var out []string
		for _, e := range ref.Efforts {
			out = append(out, e.Level)
		}
		return strings.Join(out, " ")
	}
	plan := read(compatible, "plan")
	if levels(plan) != "low medium xhigh later" || plan.Effort != "medium" || plan.Efforts[0].Description != "Quick work" || plan.Efforts[3].Description != "Not named yet" || plan.Context != 400000 {
		t.Fatalf("ChatGPT's listing: %+v", plan)
	}
	if routed := read(compatible, "routed"); levels(routed) != "minimal low medium high" || routed.Effort != "high" {
		t.Fatalf("OpenRouter's listing: %+v", routed)
	}
	if claude := read(compatible, "claude"); levels(claude) != "low medium high" || claude.Effort != "" {
		t.Fatalf("Anthropic's listing: %+v", claude)
	}
	for _, repo := range []string{"other", "silent"} {
		if ref := read(compatible, repo); len(ref.Efforts) != 0 || ref.Effort != "" {
			t.Fatalf("%s names no levels: %+v", repo, ref)
		}
	}
	if silent := read(openai, "silent"); levels(silent) != "none minimal low medium high xhigh max" || silent.Effort != "" {
		t.Fatalf("a model OpenAI's listing says nothing of takes the levels its API names: %+v", silent)
	}
	if plan := read(openai, "plan"); levels(plan) != "low medium xhigh later" || plan.Effort != "medium" {
		t.Fatalf("a model OpenAI's listing names levels for takes those: %+v", plan)
	}
	if _, err := testClient(t, compatible, &v1.Source{Id: "lab", Kind: compatible.Kind, Config: map[string]string{"endpoint": srv.URL + "/v1"}}).Resolve(withKey("lab", "k"), "hidden", ""); err == nil {
		t.Fatal("a model the listing hides is not listed")
	}
}

func TestModelsListResolveAndOpen(t *testing.T) {
	srv := modelServer(t, "k1", false)
	c := testClient(t, compatible, &v1.Source{Id: "lab", Kind: v1.SourceKind_SOURCE_KIND_OPENAI_COMPATIBLE, Config: map[string]string{"endpoint": srv.URL + "/v1"}})
	if _, err := c.Search(context.Background(), &v1.SearchRequest{}); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("a caller without a key is refused, got %v", err)
	}
	if caps := c.Capabilities(context.Background()); caps.GetTokenPresent() || caps.GetBrowse() || len(caps.GetCredentials()) != 1 || caps.GetCredentials()[0] != v1.CredentialKind_CREDENTIAL_KIND_KEY {
		t.Fatalf("capabilities without a key %v", caps)
	}
	ctx := withKey("lab", "k1")
	if caps := c.Capabilities(ctx); !caps.GetTokenPresent() || !caps.GetBrowse() {
		t.Fatalf("capabilities with a key %v", caps)
	}
	resp, err := c.Search(ctx, &v1.SearchRequest{Sort: SortName})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetHits()) != 2 || resp.Hits[0].GetRepo() != "org/large" || resp.Hits[1].GetExtra()["context"] != "8192" || resp.Hits[1].GetFormats()[0] != "openai" || Kind(resp.Hits[1]) != v1.ModelKind_MODEL_KIND_LANGUAGE {
		t.Fatalf("hits %v", resp.GetHits())
	}
	if resp, err := c.Search(ctx, &v1.SearchRequest{Filters: map[string]string{FacetFormat: "gguf"}}); err != nil || len(resp.GetHits()) != 0 {
		t.Fatalf("a format the provider does not serve lists nothing: %v %v", resp, err)
	}
	if _, err := c.Resolve(ctx, "nope", ""); err == nil {
		t.Fatal("a model the provider does not list is refused")
	}
	m, err := c.Resolve(ctx, "small", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.GetArtifacts()) != 1 || m.Artifacts[0].GetPath() != "relay.openai.json" {
		t.Fatalf("model %v", m)
	}
	// The bytes open without the caller, as a pull's task does
	blob, err := c.Open(context.Background(), m, m.Artifacts[0])
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, blob.Size())
	if _, err := blob.ReadAt(data, 0); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	ref, err := ParseReference(data)
	if err != nil || ref.Endpoint != srv.URL+"/v1" || ref.Model != "small" || ref.API != "openai" || ref.Context != 8192 || uint64(len(data)) != m.Artifacts[0].GetSizeBytes() {
		t.Fatalf("reference %+v %v", ref, err)
	}
	// Without a caller the reference still names the model, with no context
	m, err = c.Resolve(context.Background(), "small", "")
	if err != nil {
		t.Fatal(err)
	}
	var bare Reference
	blob, _ = c.Open(context.Background(), m, m.Artifacts[0])
	data = make([]byte, blob.Size())
	blob.ReadAt(data, 0)
	if err := json.Unmarshal(data, &bare); err != nil || bare.Context != 0 || bare.Model != "small" {
		t.Fatalf("bare reference %+v %v", bare, err)
	}
}

func TestAnthropicListsEveryPage(t *testing.T) {
	srv := modelServer(t, "k2", true)
	c := testClient(t, anthropic, &v1.Source{Id: "anthropic", Kind: v1.SourceKind_SOURCE_KIND_ANTHROPIC, Config: map[string]string{"endpoint": srv.URL + "/v1"}})
	resp, err := c.Search(withKey("anthropic", "k2"), &v1.SearchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetHits()) != 2 || resp.Hits[0].GetRepo() != "claude-b" || resp.Hits[1].GetName() != "Claude A" || resp.Hits[1].GetExtra()["context"] != "200000" || resp.Hits[0].GetFormats()[0] != "anthropic" {
		t.Fatalf("hits %v", resp.GetHits())
	}
	if h := anthropic.SignIn.Headers("sk", nil); h["Authorization"] != "Bearer sk" || h["anthropic-version"] == "" {
		t.Fatalf("headers %v", h)
	}
	if h := anthropic.SignIn.Headers("sk", http.Header{"Anthropic-Version": {"2026-10-21"}}); len(h) != 1 {
		t.Fatalf("a version the client sent stands, got %v", h)
	}
}

// Each provider's limit headers read with every reset format in use, a header naming no limit read as none
func TestLimitsReadEachResetFormat(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name   string
		limits *Limits
		window string
		header http.Header
		reset  time.Time
	}{
		{"anthropic rfc 3339", ProviderOf(v1.SourceKind_SOURCE_KIND_ANTHROPIC).Limits, "input-tokens", http.Header{"Anthropic-Ratelimit-Input-Tokens-Limit": {"2000000"}, "Anthropic-Ratelimit-Input-Tokens-Remaining": {"1000"}, "Anthropic-Ratelimit-Input-Tokens-Reset": {"2026-11-20T12:00:30Z"}}, now.Add(30 * time.Second)},
		{"openai duration", ProviderOf(v1.SourceKind_SOURCE_KIND_OPENAI).Limits, "tokens", http.Header{"X-Ratelimit-Limit-Tokens": {"150000"}, "X-Ratelimit-Remaining-Tokens": {"1000"}, "X-Ratelimit-Reset-Tokens": {"6m0s"}}, now.Add(6 * time.Minute)},
		{"groq fractional duration", ProviderOf(v1.SourceKind_SOURCE_KIND_GROQ).Limits, "requests", http.Header{"X-Ratelimit-Limit-Requests": {"14400"}, "X-Ratelimit-Remaining-Requests": {"1000"}, "X-Ratelimit-Reset-Requests": {"2m59.56s"}}, now.Add(2*time.Minute + 59560*time.Millisecond)},
		{"seconds", &Limits{Header: "x-{field}"}, "", http.Header{"X-Limit": {"10"}, "X-Remaining": {"1000"}, "X-Reset": {"2"}}, now.Add(2 * time.Second)},
		{"unix seconds", &Limits{Header: "x-{field}"}, "", http.Header{"X-Limit": {"10"}, "X-Remaining": {"1000"}, "X-Reset": {"1795176000"}}, time.Unix(1795176000, 0)},
		{"unix milliseconds", &Limits{Header: "x-{field}"}, "", http.Header{"X-Limit": {"10"}, "X-Remaining": {"1000"}, "X-Reset": {"1795176000500"}}, time.UnixMilli(1795176000500)},
	} {
		limit, remaining, reset, ok := c.limits.Read(c.header, c.window, now)
		if !ok || limit <= 0 || remaining != 1000 || !reset.Equal(c.reset) {
			t.Fatalf("%s: %v %v %v %v, want reset %v", c.name, limit, remaining, reset, ok, c.reset)
		}
	}
	if _, _, _, ok := ProviderOf(v1.SourceKind_SOURCE_KIND_OPENAI).Limits.Read(http.Header{}, "tokens", now); ok {
		t.Fatal("an answer stating no limit was read as one")
	}
	if ProviderOf(v1.SourceKind_SOURCE_KIND_XAI).Limits != nil {
		t.Fatal("xAI documents no limit headers")
	}
}
