package sources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestCSGHubFacetsComeFromItsBuiltInTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tags" || r.URL.RawQuery != "built_in=true&scope=model" {
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[
			{"name":"text-generation","category":"task","group":"natural_language_processing","built_in":true,"show_name":"text generation"},
			{"name":"llama","category":"task","built_in":false},
			{"name":"ocr","category":"task","group":"computer_vision","built_in":true},
			{"name":"gguf","category":"framework","built_in":true,"show_name":"GGUF"},
			{"name":"Other","category":"license","built_in":true,"show_name":"Other"},
			{"name":"other","category":"license","built_in":true},
			{"name":"en","category":"language","built_in":true,"show_name":"english"},
			{"name":"vllm","category":"runtime_framework","group":"inference","built_in":true,"show_name":"vllm"},
			{"name":"amd-evalscope","category":"runtime_framework","group":"evaluation","built_in":true},
			{"name":"ascend","category":"resource","built_in":true,"show_name":"ascend"},
			{"name":"Agriculture","category":"industry","built_in":true}
		]}`))
	}))
	defer srv.Close()
	c := testClient(t, csghub, &v1.Source{Id: "csg", Kind: v1.SourceKind_SOURCE_KIND_CSGHUB, Config: map[string]string{"endpoint": srv.URL}})
	facets := map[string]*v1.Facet{}
	var order []string
	for _, f := range c.Capabilities(context.Background()).GetFacets() {
		facets[f.GetId()] = f
		order = append(order, f.GetId())
	}
	if got, want := strings.Join(order, " "), "task domain framework license language runtime_framework resource parameters review serverless tag"; got != want {
		t.Fatalf("facets\nwant %s\n got %s", want, got)
	}
	values := func(id string) string {
		var out []string
		for _, v := range facets[id].GetValues() {
			out = append(out, v.GetId()+"="+v.GetLabel()+"@"+v.GetGroup())
		}
		return strings.Join(out, " ")
	}
	for id, want := range map[string]string{
		FacetTask:        "text-generation=text generation@Natural language processing ocr=ocr@Computer vision",
		csgFacetDomain:   "natural_language_processing=Natural language processing@ computer_vision=Computer vision@",
		FacetFramework:   "gguf=GGUF@",
		FacetLicense:     "other=Other@",
		csgFacetLanguage: "en=english@",
		csgFacetRuntime:  "vllm=vllm@Inference amd-evalscope=amd-evalscope@Evaluation",
		csgFacetResource: "ascend=ascend@",
		csgFacetReview:   "compliant=Compliant@ pending_review=Pending review@ non_compliant=Non compliant@",
	} {
		if got := values(id); got != want {
			t.Fatalf("%s values\nwant %s\n got %s", id, want, got)
		}
	}
	for id, want := range map[string]v1.FacetInput{
		csgFacetParameters: v1.FacetInput_FACET_INPUT_RANGE,
		csgFacetServerless: v1.FacetInput_FACET_INPUT_TOGGLE,
		FacetTag:           v1.FacetInput_FACET_INPUT_TEXT,
		FacetTask:          v1.FacetInput_FACET_INPUT_CHOICE,
	} {
		if facets[id].GetInput() != want {
			t.Fatalf("%s input %s", id, facets[id].GetInput())
		}
	}
	// Tags named together must all be carried, so picks of one facet are searched one at a time
	for _, f := range facets {
		if f.GetMulti() != (f.GetId() == FacetTag) {
			t.Fatalf("only typed tags go several to a request, %s multi %v", f.GetId(), f.GetMulti())
		}
	}
}

func TestCSGHubSendsEveryFilterAndOrder(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("unexpected request %s", r.URL)
		}
		w.Write([]byte(`{"total":45,"data":[{"path":"org/m","name":"m","downloads":9,"likes":2,"license":"mit",
			"compliance_status":"pending_review","commercial_permission":"conditional_commercial","hf_path":"up/m",
			"created_at":"2026-01-02T03:04:05Z","updated_at":"2026-02-02T03:04:05Z","metadata":{"model_params":0},
			"tags":[{"name":"llama","category":"task","built_in":false},{"name":"text-generation","category":"task","built_in":true},
				{"name":"gguf","category":"framework","built_in":true},{"name":"vllm","category":"runtime_framework","built_in":true},
				{"name":"sglang","category":"runtime_framework","built_in":true},{"name":"en","category":"language","built_in":true}]}]}`))
	}))
	defer srv.Close()
	c := testClient(t, csghub, &v1.Source{Id: "csg", Kind: v1.SourceKind_SOURCE_KIND_CSGHUB, Config: map[string]string{"endpoint": srv.URL}})
	resp, err := c.Search(context.Background(), &v1.SearchRequest{Query: "qwen", Sort: SortDownloads, Limit: 20, Cursor: "2", Tags: []string{"chat"}, Filters: map[string]string{
		FacetTask:          "text-generation",
		csgFacetDomain:     "audio_processing",
		FacetFramework:     "gguf",
		FacetLicense:       "mit",
		csgFacetLanguage:   "en",
		csgFacetRuntime:    "vllm",
		csgFacetResource:   "ascend",
		FacetTag:           "qwen3,moe",
		csgFacetParameters: "1.5B..8B",
		csgFacetReview:     "compliant",
		csgFacetServerless: "true",
	}})
	if err != nil {
		t.Fatal(err)
	}
	for param, want := range map[string]string{
		"sort":              "most_download",
		"per":               "20",
		"page":              "2",
		"search":            "qwen",
		"tag_category":      "task|task|framework|license|language|runtime_framework|resource|||",
		"tag_name":          "text-generation||gguf|mit|en|vllm|ascend|qwen3|moe|chat",
		"tag_group":         "|audio_processing||||||||",
		"model_params_min":  "1.5",
		"model_params_max":  "8",
		"compliance_status": "compliant",
		"list_serverless":   "true",
		"task_tag":          "",
	} {
		if v := strings.Join(got[param], "|"); v != want {
			t.Fatalf("%s\nwant %s\n got %s", param, want, v)
		}
	}
	if resp.GetTotal() != 45 || resp.GetNextCursor() != "3" || len(resp.GetHits()) != 1 {
		t.Fatalf("response %v", resp)
	}
	hit := resp.GetHits()[0]
	if hit.GetTask() != "text-generation" || hit.GetLibrary() != "gguf" || hit.GetDownloads() != 9 || hit.GetLikes() != 2 || hit.GetUpdatedAt() == nil {
		t.Fatalf("hit %v", hit)
	}
	for key, want := range map[string]string{"runtime_framework": "vllm,sglang", "languages": "en", "commercial": "Conditional commercial", "review": "Pending review", "hf_path": "up/m"} {
		if hit.GetExtra()[key] != want {
			t.Fatalf("%s %q, want %q", key, hit.GetExtra()[key], want)
		}
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{}); err != nil || got.Has("tag_category") || got.Get("sort") != "trending" {
		t.Fatalf("a bare search names no tags, got %v %v", got, err)
	}
	for facet, bad := range map[string]string{csgFacetReview: "approved", csgFacetParameters: "large"} {
		if _, err := c.Search(context.Background(), &v1.SearchRequest{Filters: map[string]string{facet: bad}}); !errors.Is(err, ErrSource) {
			t.Fatalf("%s=%s should be refused, got %v", facet, bad, err)
		}
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Sort: SortUpdated, Ascending: true}); err == nil || !strings.Contains(err.Error(), "ascending") {
		t.Fatalf("the hub orders one way only, got %v", err)
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Author: "OpenCSG"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("the hub lists no models by owner, got %v", err)
	}
}
