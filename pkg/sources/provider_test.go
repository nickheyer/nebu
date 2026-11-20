package sources

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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
