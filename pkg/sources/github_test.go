package sources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestGitHubFacetsListItsLicensesThenTheRestItsSearchTakes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/licenses" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`[{"key":"mit","name":"MIT License"},{"key":"cc-by-4.0","name":"Creative Commons Attribution 4.0 International"},{"key":"apache-2.0","name":"Apache License 2.0"}]`))
	}))
	defer srv.Close()
	c := testClient(t, github, &v1.Source{Id: "gh", Config: map[string]string{"endpoint": srv.URL}})
	caps := c.Capabilities(context.Background())
	facets := map[string]*v1.Facet{}
	var order []string
	for _, f := range caps.GetFacets() {
		facets[f.GetId()] = f
		order = append(order, f.GetId())
	}
	if got, want := strings.Join(order, " "), "license in author tag language stars forks size pushed created fork hide_archived visibility"; got != want {
		t.Fatalf("facets\nwant %s\n got %s", want, got)
	}
	var licenses []string
	for _, v := range facets[FacetLicense].GetValues() {
		licenses = append(licenses, v.GetId())
	}
	// Listed and documented licenses together once each, by name
	if len(licenses) != 2+len(ghLicenses) || licenses[0] != "afl-3.0" || licenses[1] != "apache-2.0" || strings.Count(strings.Join(licenses, " "), "cc-by-4.0") != 1 || !strings.Contains(strings.Join(licenses, " "), "other") {
		t.Fatalf("licenses %v", licenses)
	}
	inputs := map[string]v1.FacetInput{
		FacetLicense:        v1.FacetInput_FACET_INPUT_CHOICE,
		ghFacetIn:           v1.FacetInput_FACET_INPUT_CHOICE,
		ghFacetStars:        v1.FacetInput_FACET_INPUT_RANGE,
		ghFacetSize:         v1.FacetInput_FACET_INPUT_RANGE,
		ghFacetPushed:       v1.FacetInput_FACET_INPUT_TEXT,
		ghFacetHideArchived: v1.FacetInput_FACET_INPUT_TOGGLE,
		ghFacetFork:         v1.FacetInput_FACET_INPUT_CHOICE,
	}
	for id, want := range inputs {
		if facets[id].GetInput() != want {
			t.Fatalf("%s input %s", id, facets[id].GetInput())
		}
	}
	if !facets[FacetLicense].GetMulti() || !facets[ghFacetIn].GetMulti() || !facets[FacetTag].GetMulti() || !facets[ghFacetLanguage].GetMulti() || facets[ghFacetFork].GetMulti() || facets[ghFacetVisibility].GetMulti() || facets[FacetAuthor].GetMulti() {
		t.Fatal("licenses, fields, topics, and languages go several to a request, the rest one")
	}
	var sorts, columns []string
	for _, s := range caps.GetSorts() {
		sorts = append(sorts, fmt.Sprintf("%s=%s/%t", s.GetId(), s.GetLabel(), s.GetReversible()))
	}
	if got, want := strings.Join(sorts, " "), "relevance=Best match/false likes=Most stars/true forks=Most forks/true updated=Recently updated/true help_wanted=Most help wanted issues/true"; got != want {
		t.Fatalf("sorts\nwant %s\n got %s", want, got)
	}
	for _, col := range caps.GetColumns() {
		if col.GetShown() {
			columns = append(columns, col.GetLabel()+"/"+col.GetSort())
		}
	}
	if got, want := strings.Join(columns, " "), "Stars/likes Forks/forks Language/ Updated/updated"; got != want {
		t.Fatalf("shown columns\nwant %s\n got %s", want, got)
	}
}

func TestGitHubSendsEveryQualifierAndOrder(t *testing.T) {
	var got url.Values
	searches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/repositories" {
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		got = r.URL.Query()
		searches++
		w.Write([]byte(`{"total_count":5000,"items":[{"full_name":"o/r","name":"r","owner":{"login":"o"},"stargazers_count":9,"forks_count":4,
			"open_issues_count":3,"size":2,"language":"Python","fork":true,"archived":true,"license":{"spdx_id":"MIT"},"pushed_at":"2026-01-02T03:04:05Z"}]}`))
	}))
	defer srv.Close()
	c := testClient(t, github, &v1.Source{Id: "gh", Config: map[string]string{"endpoint": srv.URL}})
	resp, err := c.Search(context.Background(), &v1.SearchRequest{Query: "llama", Tags: []string{"moe"}, Sort: ghSortHelpWanted, Ascending: true, Filters: map[string]string{
		ghFacetIn:           "name,readme",
		FacetAuthor:         "meta-llama",
		FacetTag:            "llm,gguf",
		ghFacetLanguage:     "Python,Jupyter Notebook",
		FacetLicense:        "mit,other",
		ghFacetStars:        "1K..",
		ghFacetForks:        "..50",
		ghFacetSize:         "10..2000",
		ghFacetPushed:       ">=2025-01-01",
		ghFacetCreated:      "2024-01-01T00:00:00Z..*",
		ghFacetFork:         "true",
		ghFacetHideArchived: "true",
		ghFacetVisibility:   "public",
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := `llama user:meta-llama topic:llm topic:gguf topic:moe language:Python language:"Jupyter Notebook" license:mit license:other stars:>=1000 forks:<=50 size:10..2000 pushed:>=2025-01-01 created:2024-01-01T00:00:00Z..* archived:false is:public in:name,readme fork:true`
	if q := got.Get("q"); q != want {
		t.Fatalf("q\nwant %s\n got %s", want, q)
	}
	if got.Get("sort") != "help-wanted-issues" || got.Get("order") != "asc" {
		t.Fatalf("order %v", got)
	}
	hit := resp.GetHits()[0]
	for key, want := range map[string]string{"forks": "4", "open_issues": "3", "repo_size": "2048", "fork": "true", "archived": "true", "language": "Python"} {
		if hit.GetExtra()[key] != want {
			t.Fatalf("extra %s %q, want %q", key, hit.GetExtra()[key], want)
		}
	}
	if hit.GetLikes() != 9 || hit.GetLicense() != "MIT" || hit.GetAuthor() != "o" || hit.GetUpdatedAt().AsTime().Year() != 2026 {
		t.Fatalf("hit %v", hit)
	}
	// The search serves its first thousand results only, so the cursor stops there
	if resp.GetNextCursor() != "2" {
		t.Fatalf("next %q", resp.GetNextCursor())
	}
	if resp, err := c.Search(context.Background(), &v1.SearchRequest{Limit: 100, Cursor: "10"}); err != nil || resp.GetNextCursor() != "" {
		t.Fatalf("past the window %q %v", resp.GetNextCursor(), err)
	}
	// Fields need text to match and forks need something to search, so browsing them lists everything with a star
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Filters: map[string]string{ghFacetIn: "name", ghFacetFork: "only"}}); err != nil || got.Get("q") != "stars:>0 fork:only" || got.Has("sort") {
		t.Fatalf("browse %q %v", got.Get("q"), err)
	}
	// A year or a month stands for the whole of it
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Filters: map[string]string{ghFacetCreated: "2024", ghFacetPushed: "<2025-06"}}); err != nil || got.Get("q") != "pushed:<2025-06 created:2024" {
		t.Fatalf("year and month %q %v", got.Get("q"), err)
	}
	searched := searches
	for facet, bad := range map[string]string{ghFacetPushed: "last week", ghFacetCreated: "2024-1-1", ghFacetStars: "lots", ghFacetVisibility: "internal", ghFacetFork: "yes", ghFacetIn: "name,code"} {
		if _, err := c.Search(context.Background(), &v1.SearchRequest{Query: "x", Filters: map[string]string{facet: bad}}); !errors.Is(err, ErrSource) {
			t.Fatalf("%s=%s should be refused, got %v", facet, bad, err)
		}
	}
	if searches != searched {
		t.Fatal("a refused filter still searched")
	}
}
