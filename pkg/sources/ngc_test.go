package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestNGCFacetsComeFromItsTaxonomy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/search/catalog/filters/MODEL" || r.URL.Query().Get("q") != `{"query":""}` {
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`[
			{"filterCategory":"application","displayName":"Use Case","filterValues":[{"filterValue":"uscs_automatic_speech_recognition","displayName":"Automatic Speech Recognition","facetCount":87}]},
			{"filterCategory":"nvidia_nim","displayName":"NVIDIA NIM","filterValues":[{"filterValue":"nimmcro_nvidia_nim","displayName":"NVIDIA NIM","facetCount":5}]},
			{"filterCategory":"platform","displayName":"NVIDIA Platform","filterValues":[{"filterValue":"pltfm_riva","displayName":"Riva"}]},
			{"filterCategory":"productNames","displayName":"NVIDIA AI Enterprise","filterValues":[{"filterValue":"nvaie_supported","displayName":"NVIDIA AI Enterprise Supported"},{"filterValue":"ace-ea","displayName":"ace-ea"}]},
			{"filterCategory":"framework","displayName":"Framework","filterValues":[{"filterValue":"frwk_pytorch","displayName":"PyTorch"}]},
			{"filterCategory":"industry","displayName":"Industry","filterValues":[]},
			{"filterCategory":"technology","displayName":"Solution","filterValues":[{"filterValue":"soln_ai","displayName":"AI"}]},
			{"filterCategory":"publisher","displayName":"Publisher","filterValues":[{"filterValue":"nvidia","displayName":"Nvidia"},{"filterValue":"meta","displayName":"Meta"}]},
			{"filterCategory":"language","displayName":"Language","filterValues":[{"filterValue":"lng_english","displayName":"English"}]},
			{"filterCategory":"other","displayName":"Other","filterValues":[{"filterValue":"monai","displayName":"Monai"},{"filterValue":"nspect_fy5h_v6co","displayName":"Nspect Fy5h V6co"},{"filterValue":"false","displayName":"False"}]},
			{"filterCategory":"signedModels","displayName":"Signed Models","filterValues":[{"filterValue":"signed_models","displayName":"Yes"}]},
			{"filterCategory":"resourceType","displayName":"Entity Type","filterValues":[{"filterValue":"model","displayName":"Model"}]}
		]`))
	}))
	defer srv.Close()
	c := testClient(t, ngc, &v1.Source{Id: "ngc", Kind: v1.SourceKind_SOURCE_KIND_NGC, Config: map[string]string{"endpoint": srv.URL}})
	facets := map[string]*v1.Facet{}
	var order []string
	for _, f := range c.Capabilities(context.Background()).GetFacets() {
		facets[f.GetId()] = f
		order = append(order, f.GetId())
	}
	// An empty category is left out, and the framework category gives way to the typed framework
	if got, want := strings.Join(order, " "), "task publisher platform solution language nim product other guest signed framework precision tag"; got != want {
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
		FacetTask:        "uscs_automatic_speech_recognition=Automatic Speech Recognition",
		FacetPublisher:   "nvidia=Nvidia meta=Meta",
		ngcFacetSolution: "soln_ai=AI",
		ngcFacetOther:    "monai=Monai",
	} {
		if got := values(id); got != want {
			t.Fatalf("%s values\nwant %s\n got %s", id, want, got)
		}
	}
	if facets[FacetTask].GetLabel() != "Use Case" || facets[ngcFacetSolution].GetLabel() != "Solution" {
		t.Fatalf("labels come from the catalog, got %q %q", facets[FacetTask].GetLabel(), facets[ngcFacetSolution].GetLabel())
	}
	if !facets[FacetTask].GetMulti() || !facets[FacetPublisher].GetMulti() || facets[ngcFacetProduct].GetMulti() {
		t.Fatal("taxonomy values go several to a request, products one each")
	}
	inputs := map[string]v1.FacetInput{
		FacetTask:         v1.FacetInput_FACET_INPUT_CHOICE,
		ngcFacetGuest:     v1.FacetInput_FACET_INPUT_TOGGLE,
		ngcFacetSigned:    v1.FacetInput_FACET_INPUT_TOGGLE,
		FacetFramework:    v1.FacetInput_FACET_INPUT_TEXT,
		ngcFacetPrecision: v1.FacetInput_FACET_INPUT_TEXT,
		FacetTag:          v1.FacetInput_FACET_INPUT_TEXT,
	}
	for id, want := range inputs {
		if facets[id].GetInput() != want {
			t.Fatalf("%s input %s", id, facets[id].GetInput())
		}
	}
	if !facets[FacetFramework].GetMulti() || !facets[ngcFacetPrecision].GetMulti() || !facets[FacetTag].GetMulti() {
		t.Fatal("every typed framework, precision, and label goes in one request")
	}
}

func TestNGCSendsEveryFilterAndOrder(t *testing.T) {
	type term struct{ Field, Value string }
	var got struct {
		Query    string `json:"query"`
		Page     int    `json:"page"`
		PageSize int    `json:"pageSize"`
		OrderBy  []term `json:"orderBy"`
		Filters  []term `json:"filters"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/search/catalog/filters/MODEL":
			w.Write([]byte(`[]`))
			return
		case "/v2/search/catalog/resources/MODEL":
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
		if err := json.Unmarshal([]byte(r.URL.Query().Get("q")), &got); err != nil {
			t.Errorf("query %v", err)
		}
		w.Write([]byte(`{"resultPageTotal":3,"resultTotal":7,"results":[{"groupValue":"MODEL","resources":[{
			"resourceType":"MODEL","resourceId":"nvidia/riva/parakeet","orgName":"nvidia","teamName":"riva","name":"parakeet","displayName":"Parakeet",
			"isPublic":true,"weightPopular":2210.6,"dateModified":"2025-01-02T03:04:05.000Z",
			"labels":[{"key":"framework","values":["Other","NeMo"]},{"key":"precision","values":["FP16","n/a"]},{"key":"publisher","values":["NVIDIA"]},{"key":"hasSignedVersion","values":["true"]},{"key":"general","values":["Riva","NSPECT-ABCD-1234"]}],
			"attributes":[{"key":"latestVersionSizeInBytes","value":"2338476221"},{"key":"latestVersionIdStr","value":"trainable_v3.0"},{"key":"application","value":"Speech To Text"},{"key":"format","value":"riva"}]}]}]}`))
	}))
	defer srv.Close()
	c := testClient(t, ngc, &v1.Source{Id: "ngc", Kind: v1.SourceKind_SOURCE_KIND_NGC, Config: map[string]string{"endpoint": srv.URL}})
	resp, err := c.Search(context.Background(), &v1.SearchRequest{Query: "parakeet", Sort: SortUpdated, Ascending: true, Cursor: "1", Limit: 5, Filters: map[string]string{
		FacetTask:         "uscs_automatic_speech_recognition,uscs_text_to_speech",
		FacetPublisher:    "nvidia",
		ngcFacetPlatform:  "pltfm_riva",
		ngcFacetIndustry:  "indus_healthcare",
		ngcFacetSolution:  "soln_ai",
		ngcFacetLanguage:  "lng_english",
		ngcFacetNIM:       "nimmcro_nvidia_nim",
		ngcFacetProduct:   "ace-ea",
		ngcFacetOther:     "monai",
		FacetFramework:    "PyTorch,NeMo",
		ngcFacetPrecision: "FP16,BF16",
		FacetTag:          "Medical Imaging",
		ngcFacetGuest:     "true",
		ngcFacetSigned:    "true",
	}})
	if err != nil {
		t.Fatal(err)
	}
	var filters []string
	for _, f := range got.Filters {
		filters = append(filters, f.Field+"="+f.Value)
	}
	want := "application=uscs_automatic_speech_recognition application=uscs_text_to_speech publisher=nvidia platform=pltfm_riva industry=indus_healthcare technology=soln_ai language=lng_english nvidia_nim=nimmcro_nvidia_nim productNames=ace-ea other=monai " +
		"framework=PyTorch framework=NeMo precision=fp16 precision=bf16 label=Medical Imaging guestAccess=true signedModels=true"
	if got := strings.Join(filters, " "); got != want {
		t.Fatalf("filters\nwant %s\n got %s", want, got)
	}
	if got.Query != "parakeet" || got.Page != 1 || got.PageSize != 5 || len(got.OrderBy) != 1 || got.OrderBy[0] != (term{"dateModified", "ASC"}) {
		t.Fatalf("query %+v", got)
	}
	if resp.GetNextCursor() != "2" || resp.GetTotal() != 7 || len(resp.GetHits()) != 1 {
		t.Fatalf("response %v", resp)
	}
	hit := resp.GetHits()[0]
	extra := hit.GetExtra()
	if hit.GetTask() != "Speech To Text" || hit.GetAuthor() != "NVIDIA" || !hit.GetGated() || hit.GetSizeBytes() != 2338476221 || hit.GetUpdatedAt() == nil {
		t.Fatalf("hit %v", hit)
	}
	for k, want := range map[string]string{"framework": "NeMo", "precision": "FP16", "format": "riva", "version": "trainable_v3.0", "popularity": "2211", "signed": "true"} {
		if extra[k] != want {
			t.Fatalf("extra %s want %q got %q", k, want, extra[k])
		}
	}
	if strings.Join(hit.GetTags(), ",") != "NeMo,FP16,NVIDIA,Riva" {
		t.Fatalf("tags %v", hit.GetTags())
	}
	// A toggle switched off sends nothing, and popularity stands in for relevance without a query
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Sort: SortRelevance, Filters: map[string]string{ngcFacetGuest: "false"}}); err != nil || len(got.Filters) != 0 || got.OrderBy[0] != (term{"weightPopular", "DESC"}) {
		t.Fatalf("filters %v order %v %v", got.Filters, got.OrderBy, err)
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Sort: SortName}); err != nil || got.OrderBy[0] != (term{"name", "ASC"}) {
		t.Fatalf("names run a to z, got %v %v", got.OrderBy, err)
	}
	var sorts []string
	for _, s := range c.Capabilities(context.Background()).GetSorts() {
		sorts = append(sorts, s.GetId()+"="+s.GetLabel())
	}
	if sorts[0] != SortDownloads+"=Most popular" {
		t.Fatalf("sorts %v", sorts)
	}
}
