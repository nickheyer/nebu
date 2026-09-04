package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A server that answers everything with 404
func notFoundServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Builds a client for a catalog copy, usually pointed at a test server
func testClient(t *testing.T, cat Catalog, spec *v1.Source) *Client {
	t.Helper()
	c, err := newClient(&cat, spec)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBuildListsEveryCatalogThenConfig(t *testing.T) {
	r, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range r.List() {
		ids = append(ids, s.GetId())
	}
	if got := strings.Join(ids, ","); got != "huggingface,modelscope,ollama,civitai,dockerhub,kaggle,ngc,csghub" {
		t.Fatalf("built in order %s", got)
	}
	if first, _ := r.Get(""); first.Spec().GetId() != "huggingface" || first.Spec().GetKind() != v1.SourceKind_SOURCE_KIND_HUGGINGFACE || first.Spec().GetEndpoint() == "" {
		t.Fatalf("fallback %+v", first.Spec())
	}
	for _, st := range r.Statuses(context.Background()) {
		if st.GetCapabilities().GetDefaultSort() == "" || st.GetCapabilities().GetRepoPattern() == "" || st.GetCapabilities().GetDescription() == "" {
			t.Fatalf("capabilities %+v", st)
		}
	}
	root := t.TempDir()
	r, err = Build([]*v1.Source{{Id: "disk", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	if first, _ := r.Get(""); first.Spec().GetId() != "disk" || len(r.List()) != 9 {
		t.Fatalf("configured first %v", r.List())
	}
	if _, err := r.Get("nope"); err == nil {
		t.Fatal("unknown id should fail")
	}
	for _, bad := range [][]*v1.Source{
		{{Id: "hf", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE}},
		{{Id: "x", Kind: v1.SourceKind_SOURCE_KIND_LOCAL}},
		{{Id: "a", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root}, {Id: "a", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root}},
		{{Id: "huggingface", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root}},
		{{Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root}},
		{{Id: "u", Kind: v1.SourceKind_SOURCE_KIND_UNSPECIFIED}},
	} {
		if _, err := Build(bad); err == nil {
			t.Fatalf("%v should fail", bad)
		}
	}
}

func TestClientChecksSortsAndStampsIds(t *testing.T) {
	cat := *local
	c := testClient(t, cat, &v1.Source{Id: "d", Path: t.TempDir()})
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Sort: "bogus"}); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown sort %v", err)
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Sort: SortName, Ascending: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Revisions(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), ErrUnsupported.Error()) {
		t.Fatalf("revisions %v", err)
	}
	if _, err := c.Card(context.Background(), "x", ""); err == nil || !strings.Contains(err.Error(), ErrUnsupported.Error()) {
		t.Fatalf("card %v", err)
	}
	caps := c.Capabilities(context.Background())
	if caps.GetCard() || caps.GetRevisions() || !caps.GetBrowse() || caps.GetWebUrl() == "" || len(caps.GetSorts()) != 2 || !caps.GetSorts()[0].GetReversible() {
		t.Fatalf("caps %+v", caps)
	}
	strict := *huggingface
	strict.Endpoint = notFoundServer(t).URL
	c = testClient(t, strict, &v1.Source{Id: "hf"})
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Ascending: true}); err == nil || !strings.Contains(err.Error(), "ascending") {
		t.Fatalf("ascending %v", err)
	}
	for _, s := range c.Capabilities(context.Background()).GetSorts() {
		if s.GetReversible() {
			t.Fatalf("hub sort %s should not be reversible", s.GetId())
		}
	}
}
