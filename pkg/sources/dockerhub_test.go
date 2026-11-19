package sources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestDockerHubFacetsComeFromItsCategories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/categories" {
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`[{"name":"Machine learning & AI","slug":"machine-learning-and-ai"},{"name":"Data science","slug":"data-science"}]`))
	}))
	defer srv.Close()
	c := testClient(t, dockerhub, &v1.Source{Id: "dh", Kind: v1.SourceKind_SOURCE_KIND_OCI, Config: map[string]string{"endpoint": srv.URL}})
	caps := c.Capabilities(context.Background())
	facets := map[string]*v1.Facet{}
	var order []string
	for _, f := range caps.GetFacets() {
		facets[f.GetId()] = f
		order = append(order, f.GetId())
	}
	if got, want := strings.Join(order, " "), "categories badges author"; got != want {
		t.Fatalf("facets\nwant %s\n got %s", want, got)
	}
	values := func(id string) string {
		var out []string
		for _, v := range facets[id].GetValues() {
			out = append(out, v.GetId()+"="+v.GetLabel())
		}
		return strings.Join(out, " ")
	}
	for id, want := range map[string]string{
		dhFacetCategory: "machine-learning-and-ai=Machine learning & AI data-science=Data science",
		dhFacetBadge:    "verified_publisher=Verified Publisher open_source=Sponsored OSS none=Community",
	} {
		if got := values(id); got != want {
			t.Fatalf("%s values\nwant %s\n got %s", id, want, got)
		}
	}
	if !facets[dhFacetCategory].GetMulti() || !facets[dhFacetBadge].GetMulti() || !facets[FacetAuthor].GetMulti() || facets[FacetAuthor].GetInput() != v1.FacetInput_FACET_INPUT_TEXT {
		t.Fatal("categories, badges, and typed namespaces all go several to a request")
	}
	var sorts []string
	for _, s := range caps.GetSorts() {
		sorts = append(sorts, fmt.Sprintf("%s=%s/%t", s.GetId(), s.GetLabel(), s.GetReversible()))
	}
	if got, want := strings.Join(sorts, " "), "downloads=Pull count/true relevance=Best match/false updated=Recently updated/true"; got != want {
		t.Fatalf("sorts\nwant %s\n got %s", want, got)
	}
	// A source naming only a registry lists no categories
	bare := testClient(t, dockerhub, &v1.Source{Id: "reg", Kind: v1.SourceKind_SOURCE_KIND_OCI, Config: map[string]string{"registry_endpoint": srv.URL}})
	if got := len(bare.Capabilities(context.Background()).GetFacets()); got != len(dockerhub.Facets) {
		t.Fatalf("bare registry facets %d", got)
	}
}

func TestDockerHubSendsEveryFilterAndOrder(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search/v4" {
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		got = r.URL.Query()
		w.Write([]byte(`{"total":1,"results":[{"id":"unsloth/qwen3-gguf","name":"unsloth/qwen3-gguf","publisher":{"name":"unsloth"},"badge":"open_source",
			"star_count":2,"pull_count":"4.5K","raw_pull_count":4498,"content_types":["model"],
			"categories":[{"slug":"machine-learning-and-ai","name":"Machine learning & AI"},{"slug":"data-science","name":"Data science"}]}]}`))
	}))
	defer srv.Close()
	c := testClient(t, dockerhub, &v1.Source{Id: "dh", Kind: v1.SourceKind_SOURCE_KIND_OCI, Config: map[string]string{"endpoint": srv.URL}})
	search := func(req *v1.SearchRequest) *v1.SearchResponse {
		t.Helper()
		resp, err := c.Search(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	// The parameters a filter or an order fills, in a fixed order
	params := func() string {
		var out []string
		for _, n := range []string{"query", "namespaces", "badges", "categories", "sort", "order"} {
			if got.Has(n) {
				out = append(out, n+"="+got.Get(n))
			}
		}
		return strings.Join(out, " ")
	}
	for _, tc := range []struct {
		name string
		req  *v1.SearchRequest
		want string
	}{
		{"browsing lists Docker's own namespace", &v1.SearchRequest{}, "query= namespaces=ai sort=pull_count order=desc"},
		{"a namespace before a slash scopes the query", &v1.SearchRequest{Query: "unsloth/qwen3", Filters: map[string]string{FacetAuthor: "ai"}}, "query=qwen3 namespaces=unsloth sort=pull_count order=desc"},
		{"every namespace asked", &v1.SearchRequest{Author: "k33g", Filters: map[string]string{FacetAuthor: "unsloth,k33g,aistaging"}}, "query= namespaces=k33g,unsloth,aistaging sort=pull_count order=desc"},
		{"badges search every namespace", &v1.SearchRequest{Query: "qwen", Sort: SortUpdated, Ascending: true, Filters: map[string]string{dhFacetBadge: "open_source,none"}}, "query=qwen badges=open_source,none sort=updated_at order=asc"},
		{"categories search every namespace", &v1.SearchRequest{Sort: SortRelevance, Filters: map[string]string{dhFacetCategory: "machine-learning-and-ai,data-science"}}, "query= categories=machine-learning-and-ai,data-science"},
	} {
		search(tc.req)
		if sent := params(); sent != tc.want || got.Get("type") != "model" {
			t.Fatalf("%s\nwant %s\n got %s type=%s", tc.name, tc.want, sent, got.Get("type"))
		}
	}
	hit := search(&v1.SearchRequest{}).GetHits()[0]
	if hit.GetExtra()["badge"] != "Sponsored OSS" || hit.GetExtra()[dhFacetCategory] != "Machine learning & AI,Data science" || hit.GetDownloads() != 4498 || hit.GetLikes() != 2 || hit.GetAuthor() != "unsloth" {
		t.Fatalf("hit %v", hit)
	}
}
