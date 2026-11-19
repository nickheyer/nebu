package sources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestCivitaiFacetsComeFromItsEnumsAndSortsSayWhatTheyAre(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/enums" {
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"ModelType":["Checkpoint","LORA","Audio"],"BaseModel":["SD 1.5","SDXL 1.0"],"ActiveBaseModel":["SDXL 1.0"]}`))
	}))
	defer srv.Close()
	c := testClient(t, civitai, &v1.Source{Id: "civ", Kind: v1.SourceKind_SOURCE_KIND_CIVITAI, Config: map[string]string{"endpoint": srv.URL}})
	caps := c.Capabilities(context.Background())
	facets := map[string]*v1.Facet{}
	var order []string
	for _, f := range caps.GetFacets() {
		facets[f.GetId()] = f
		order = append(order, f.GetId())
	}
	if got, want := strings.Join(order, " "), "type base_model period checkpoint file_format commercial_use access trained new_creators nsfw tag author"; got != want {
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
		FacetType:          "Checkpoint=Checkpoint LORA=LORA Audio=Audio",
		FacetBaseModel:     "SD 1.5=SD 1.5 SDXL 1.0=SDXL 1.0",
		FacetPeriod:        "Day=Last day Week=Last week Month=Last month Year=Last year",
		civFacetCheckpoint: "Trained=Trained Merge=Merged",
		civFacetAccess:     "hidePaid=Free earlyAccess=Early access paidAccess=Paid onSale=On sale",
		civFacetCommercial: "Image=Sell images RentCivit=Run on Civitai Rent=Run on paid services Sell=Sell the model SellMerge=Sell merges",
	} {
		if got := values(id); got != want {
			t.Fatalf("%s values\nwant %s\n got %s", id, want, got)
		}
	}
	for id, want := range map[string]v1.FacetInput{
		FacetType:       v1.FacetInput_FACET_INPUT_CHOICE,
		FacetNSFW:       v1.FacetInput_FACET_INPUT_TOGGLE,
		civFacetTrained: v1.FacetInput_FACET_INPUT_TOGGLE,
		FacetTag:        v1.FacetInput_FACET_INPUT_TEXT,
		FacetAuthor:     v1.FacetInput_FACET_INPUT_TEXT,
	} {
		if facets[id].GetInput() != want {
			t.Fatalf("%s input %s", id, facets[id].GetInput())
		}
	}
	for id, multi := range map[string]bool{FacetType: true, FacetBaseModel: true, civFacetFileFormat: true, civFacetCommercial: true, FacetPeriod: false, civFacetCheckpoint: false, civFacetAccess: false, FacetTag: false} {
		if facets[id].GetMulti() != multi {
			t.Fatalf("%s multi %t", id, facets[id].GetMulti())
		}
	}
	var sorts []string
	for _, s := range caps.GetSorts() {
		sorts = append(sorts, s.GetId()+"="+s.GetLabel())
		if s.GetReversible() != (s.GetId() == SortUpdated) {
			t.Fatalf("only the newest order flips, %s says %t", s.GetId(), s.GetReversible())
		}
	}
	if got, want := strings.Join(sorts, ","), "downloads=Most downloaded,relevance=Relevance,trending=Trending,likes=Most liked,comments=Most discussed,collected=Most collected,images=Most images,updated=Recently updated"; got != want {
		t.Fatalf("sorts\nwant %s\n got %s", want, got)
	}
}

func TestCivitaiSendsEveryFilterAndOrder(t *testing.T) {
	var got url.Values
	items := `{"items":[{"id":7,"name":"m","type":"Checkpoint","nsfw":false,"tags":["Anime","style"],
		"baseModels":["Pony","SDXL 1.0"],"allowNoCredit":true,"allowDerivatives":false,"allowCommercialUse":["Image","None","Sell"],
		"supportsGeneration":true,"hasActivePaidAccess":true,
		"stats":{"downloadCount":5,"thumbsUpCount":3,"commentCount":12},"creator":{"username":"someone"},
		"modelVersions":[{"id":2,"name":"v2","baseModel":"Pony","publishedAt":"2026-01-02T00:00:00Z","files":[]},
			{"id":1,"name":"v1","baseModel":"SDXL 1.0","publishedAt":"2026-03-04T00:00:00Z","files":[]}]}],
		"metadata":{"nextCursor":"7|1"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("unexpected request %s", r.URL)
		}
		got = r.URL.Query()
		w.Write([]byte(items))
	}))
	defer srv.Close()
	c := testClient(t, civitai, &v1.Source{Id: "civ", Kind: v1.SourceKind_SOURCE_KIND_CIVITAI, Config: map[string]string{"endpoint": srv.URL}})
	resp, err := c.Search(context.Background(), &v1.SearchRequest{Query: "pony", Sort: civSortComments, Filters: map[string]string{
		FacetType:           "Checkpoint,LORA",
		FacetBaseModel:      "SDXL 1.0,Pony",
		FacetPeriod:         "Week",
		civFacetCheckpoint:  "Merge",
		civFacetFileFormat:  "GGUF",
		civFacetCommercial:  "Sell,RentCivit",
		civFacetAccess:      "earlyAccess",
		civFacetTrained:     "true",
		civFacetNewCreators: "true",
		FacetNSFW:           "true",
		FacetTag:            "anime",
		FacetAuthor:         "someone",
	}})
	if err != nil {
		t.Fatal(err)
	}
	for param, want := range map[string]string{
		"query":              "pony",
		"sort":               "Most Discussed",
		"types":              "Checkpoint LORA",
		"baseModels":         "SDXL 1.0 Pony",
		"period":             "Week",
		"checkpointType":     "Merge",
		"fileFormats":        "GGUF GGUF",
		"allowCommercialUse": "Sell RentCivit",
		"earlyAccess":        "true",
		"fromPlatform":       "true",
		"newCreators":        "true",
		"nsfw":               "true",
		"tag":                "anime",
		"username":           "someone",
	} {
		if v := strings.Join(got[param], " "); v != want {
			t.Fatalf("%s\nwant %s\n got %s", param, want, v)
		}
	}
	hit := resp.GetHits()[0]
	for key, want := range map[string]string{
		"comments":       "12",
		"base_model":     "Pony,SDXL 1.0",
		"commercial_use": "Sell images,Sell the model",
		"no_credit":      "true",
		"merges":         "",
		"generation":     "true",
		"paid":           "true",
		"nsfw":           "",
		"version":        "v2",
	} {
		if v := hit.GetExtra()[key]; v != want {
			t.Fatalf("extra %s: want %q, got %q", key, want, v)
		}
	}
	if !hit.GetUpdatedAt().AsTime().Equal(time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)) || hit.GetTask() != "Checkpoint" || resp.GetNextCursor() != "7|1" {
		t.Fatalf("updated is the newest version's release, got %v, task %q, cursor %q", hit.GetUpdatedAt().AsTime(), hit.GetTask(), resp.GetNextCursor())
	}
	// Relevance names no order, adult models stay hidden, and switches left off are not sent
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Sort: SortRelevance, Query: "pony", Filters: map[string]string{civFacetTrained: "false", civFacetFileFormat: "GGUF,Diffusers"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["sort"]; ok || got.Get("nsfw") != "false" || got.Has("fromPlatform") || strings.Join(got["fileFormats"], " ") != "GGUF Diffusers" {
		t.Fatalf("relevance request %v", got)
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Sort: SortUpdated, Ascending: true}); err != nil || got.Get("sort") != "Oldest" {
		t.Fatalf("ascending newest is Oldest, got %q %v", got.Get("sort"), err)
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Filters: map[string]string{civFacetAccess: "free"}}); !errors.Is(err, ErrSource) {
		t.Fatalf("an access the API has no switch for should be refused, got %v", err)
	}
	// A tag the API does not know is dropped and every model listed, which a hit without it gives away
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Filters: map[string]string{FacetTag: "nonesuch"}}); !errors.Is(err, ErrSource) || !strings.Contains(err.Error(), "nonesuch") {
		t.Fatalf("a dropped tag should be refused, got %v", err)
	}
}
