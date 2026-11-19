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

func TestHubResolvesTheDefaultRevisionFromTheHub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/models/o/m":
			w.Write([]byte(`{"id":"o/m","sha":"abc123"}`))
		case r.URL.Path == "/api/models/o/m/revision/dev":
			w.Write([]byte(`{"id":"o/m","sha":"abc123"}`))
		case r.URL.Path == "/api/models/o/m/refs":
			w.Write([]byte(`{"branches":[{"name":"main","targetCommit":"000"},{"name":"dev","targetCommit":"abc123"}],"tags":[{"name":"v1","targetCommit":"111"}]}`))
		case r.URL.Path == "/api/models/o/m/tree/dev":
			w.Write([]byte(`[{"type":"file","path":"a.gguf","size":5,"lfs":{"oid":"deadbeef","size":5}},{"type":"directory","path":"sub"}]`))
		case r.URL.Path == "/api/models/o/m/tree/abc123":
			w.Write([]byte(`[{"type":"file","path":"a.gguf","size":5}]`))
		case r.URL.Path == "/o/m/raw/dev/README.md":
			w.Write([]byte("# hello"))
		case r.URL.Path == "/api/models/o/detached":
			w.Write([]byte(`{"id":"o/detached","sha":"fff"}`))
		case r.URL.Path == "/api/models/o/detached/refs":
			w.Write([]byte(`{"branches":[{"name":"main","targetCommit":"000"}],"tags":[]}`))
		case r.URL.Path == "/api/models/o/detached/tree/fff":
			w.Write([]byte(`[{"type":"file","path":"b.bin","size":1}]`))
		case r.URL.Path == "/api/models/o/empty":
			w.Write([]byte(`{"id":"o/empty"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := testClient(t, huggingface, &v1.Source{Id: "hf", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Config: map[string]string{"endpoint": srv.URL}})
	model, err := c.Resolve(context.Background(), "o/m", "")
	if err != nil {
		t.Fatal(err)
	}
	if model.GetRevision() != "dev" || model.GetCommit() != "abc123" || len(model.GetArtifacts()) != 1 {
		t.Fatalf("default revision %+v", model)
	}
	if a := model.GetArtifacts()[0]; a.GetSha256() != "deadbeef" || a.GetUrl() != srv.URL+"/o/m/resolve/dev/a.gguf" {
		t.Fatalf("artifact %+v", a)
	}
	revs, err := c.Revisions(context.Background(), "o/m")
	if err != nil {
		t.Fatal(err)
	}
	var def []string
	for _, r := range revs {
		if r.GetDefault() {
			def = append(def, r.GetName())
		}
	}
	if len(revs) != 3 || strings.Join(def, ",") != "dev" || revs[2].GetDetail() != "tag" {
		t.Fatalf("revisions %v", revs)
	}
	card, err := c.Card(context.Background(), "o/m", "")
	if err != nil || card.GetMarkdown() != "# hello" {
		t.Fatalf("card %v %v", card, err)
	}
	// A default commit no branch points at is served by its sha
	model, err = c.Resolve(context.Background(), "o/detached", "")
	if err != nil || model.GetRevision() != "fff" || model.GetCommit() != "fff" {
		t.Fatalf("detached %+v %v", model, err)
	}
	if _, err := c.Resolve(context.Background(), "o/empty", ""); err == nil {
		t.Fatal("a hub that names no commit should fail")
	}
}

func TestHubFacetsComeFromItsTaxonomyAndItsOwnErrors(t *testing.T) {
	var languages []string
	for i := range hubLanguages + 5 {
		languages = append(languages, fmt.Sprintf(`{"id":"l%d","label":"Language %d"}`, i, i))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/api/models-tags-by-type" && len(q) == 0:
			fmt.Fprintf(w, `{
				"pipeline_tag":[{"id":"text-generation","label":"Text Generation","subType":"nlp"}],
				"library":[{"id":"gguf","label":"GGUF"}],
				"license":[{"id":"license:mit","label":"mit"}],
				"language":[%s],
				"other":[{"id":"merge","label":"Merge"}],
				"deploy":[{"id":"endpoints_compatible","label":"Inference Endpoints"},{"id":"deploy:azure","label":"Microsoft Foundry"}],
				"bucket":[{"id":"bucket:a/b","label":"a/b"}],
				"dataset":[{"id":"dataset:squad","label":"squad"}]
			}`, strings.Join(languages, ","))
		case r.URL.Path == "/api/models" && q.Get("apps") == "-":
			http.Error(w, `{"error":"✖ Invalid option: expected one of \"llama.cpp\"|\"lmstudio\"|\"brand-new\"\n  → at [0]"}`, http.StatusBadRequest)
		case r.URL.Path == "/api/models" && q.Get("inference_provider") == "-":
			// A hub that explains nothing leaves the known providers standing
			http.Error(w, `{"error":"no"}`, http.StatusBadRequest)
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := testClient(t, huggingface, &v1.Source{Id: "hf", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Config: map[string]string{"endpoint": srv.URL}})
	facets := map[string]*v1.Facet{}
	var order []string
	for _, f := range c.Capabilities(context.Background()).GetFacets() {
		facets[f.GetId()] = f
		order = append(order, f.GetId())
	}
	if got, want := strings.Join(order, " "), "task library license language other deploy app inference_provider gated inference region parameters author tag base_model dataset"; got != want {
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
		FacetTask:      "text-generation=Text Generation",
		FacetLicense:   "mit=mit",
		hubFacetDeploy: "endpoints_compatible=Inference Endpoints deploy:azure=Microsoft Foundry",
		hubFacetApp:    "llama.cpp=llama.cpp lmstudio=LM Studio brand-new=Brand new",
		hubFacetGated:  "false=Open true=Gated",
	} {
		if got := values(id); got != want {
			t.Fatalf("%s values\nwant %s\n got %s", id, want, got)
		}
	}
	if got := facets[FacetTask].GetValues()[0].GetGroup(); got != "nlp" {
		t.Fatalf("a task keeps the hub's grouping, got %q", got)
	}
	if got := len(facets[hubFacetLanguage].GetValues()); got != hubLanguages {
		t.Fatalf("the most used %d languages, got %d", hubLanguages, got)
	}
	if got := len(facets[hubFacetProvider].GetValues()); got != len(hubProviders) {
		t.Fatalf("the known providers stand in, got %d", got)
	}
	inputs := map[string]v1.FacetInput{
		FacetTask:          v1.FacetInput_FACET_INPUT_CHOICE,
		hubFacetParameters: v1.FacetInput_FACET_INPUT_RANGE,
		FacetAuthor:        v1.FacetInput_FACET_INPUT_TEXT,
		FacetTag:           v1.FacetInput_FACET_INPUT_TEXT,
	}
	for id, want := range inputs {
		if facets[id].GetInput() != want {
			t.Fatalf("%s input %s", id, facets[id].GetInput())
		}
	}
	if !facets[hubFacetApp].GetMulti() || !facets[hubFacetProvider].GetMulti() || !facets[FacetTag].GetMulti() || facets[FacetTask].GetMulti() || facets[FacetAuthor].GetMulti() {
		t.Fatal("apps, providers, and tags go several to a request, a task and an author one")
	}
}

func TestHubSendsEveryFilterAndOrder(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Write([]byte(`[{"id":"org/m","downloads":7,"downloadsAllTime":90,"gated":"auto",
			"tags":["gguf","license:mit","base_model:org/base","base_model:quantized:org/base","region:us"]}]`))
	}))
	defer srv.Close()
	c := testClient(t, huggingface, &v1.Source{Id: "hf", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Config: map[string]string{"endpoint": srv.URL}})
	resp, err := c.Search(context.Background(), &v1.SearchRequest{Sort: SortParameters, Ascending: true, Filters: map[string]string{
		FacetTask:          "text-generation",
		FacetLibrary:       "gguf",
		FacetLicense:       "mit",
		hubFacetLanguage:   "fr",
		hubFacetOther:      "merge",
		hubFacetDeploy:     "deploy:azure",
		hubFacetRegion:     "eu",
		FacetBaseModel:     "org/base",
		hubFacetDataset:    "dataset:squad",
		FacetTag:           "conversational,moe",
		hubFacetGated:      "false",
		hubFacetInference:  "warm",
		hubFacetApp:        "ollama,lmstudio",
		hubFacetProvider:   "groq,cerebras",
		hubFacetParameters: "1B..8B",
	}})
	if err != nil {
		t.Fatal(err)
	}
	for param, want := range map[string]string{
		"sort":               "num_parameters",
		"direction":          "1",
		"pipeline_tag":       "text-generation",
		"library":            "gguf",
		"gated":              "false",
		"inference":          "warm",
		"apps":               "ollama,lmstudio",
		"inference_provider": "groq,cerebras",
		"num_parameters":     "min:1000000000,max:8000000000",
		"filter":             "license:mit fr merge deploy:azure region:eu base_model:org/base dataset:squad conversational moe",
	} {
		if v := strings.Join(got[param], " "); v != want {
			t.Fatalf("%s\nwant %s\n got %s", param, want, v)
		}
	}
	hit := resp.GetHits()[0]
	if !hit.GetGated() || hit.GetLicense() != "mit" || hit.GetExtra()["base_model"] != "org/base" || hit.GetExtra()["downloads_all_time"] != "90" {
		t.Fatalf("hit %v", hit)
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Filters: map[string]string{hubFacetParameters: "..8B"}}); err != nil || got.Get("num_parameters") != "max:8000000000" || got.Get("direction") != "-1" {
		t.Fatalf("an open range sends one side, got %q %v", got.Get("num_parameters"), err)
	}
	for facet, bad := range map[string]string{hubFacetParameters: "large", hubFacetGated: "maybe"} {
		if _, err := c.Search(context.Background(), &v1.SearchRequest{Filters: map[string]string{facet: bad}}); !errors.Is(err, ErrSource) {
			t.Fatalf("%s=%s should be refused, got %v", facet, bad, err)
		}
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Sort: hubSortLikesWeek}); err != nil || got.Get("sort") != "likes7d" {
		t.Fatalf("weekly likes sort %q %v", got.Get("sort"), err)
	}
}

func TestModelScopeResolvesTheDefaultRevisionFromTheHub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/models/o/m" && r.URL.RawQuery == "":
			w.Write([]byte(`{"Code":200,"Data":{"Name":"m","Revision":"release"}}`))
		case r.URL.Path == "/api/v1/models/o/m/repo/files":
			if r.URL.Query().Get("Revision") != "release" {
				http.Error(w, "wrong revision "+r.URL.Query().Get("Revision"), http.StatusBadRequest)
				return
			}
			w.Write([]byte(`{"Code":200,"Data":{"Files":[{"Path":"a.gguf","Size":5,"Sha256":"aa","Type":"blob","Revision":"r1"},{"Path":"dir","Type":"tree"}]}}`))
		case r.URL.Path == "/api/v1/models/o/m/revisions":
			w.Write([]byte(`{"Code":200,"Data":{"RevisionMap":{"Branches":[{"Revision":"master","CreatedAt":1},{"Revision":"release","CreatedAt":2}],"Tags":[{"Revision":"v1","CreatedAt":3}]}}}`))
		case r.URL.Path == "/api/v1/models/o/bare":
			w.Write([]byte(`{"Code":200,"Data":{"Name":"bare"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := testClient(t, modelscope, &v1.Source{Id: "ms", Kind: v1.SourceKind_SOURCE_KIND_MODELSCOPE, Config: map[string]string{"endpoint": srv.URL}})
	model, err := c.Resolve(context.Background(), "o/m", "")
	if err != nil {
		t.Fatal(err)
	}
	if model.GetRevision() != "release" || model.GetCommit() != "r1" || len(model.GetArtifacts()) != 1 {
		t.Fatalf("model %+v", model)
	}
	if u := model.GetArtifacts()[0].GetUrl(); !strings.Contains(u, "/api/v1/models/o/m/repo?") || !strings.Contains(u, "FilePath=a.gguf") || !strings.Contains(u, "Revision=release") {
		t.Fatalf("url %s", u)
	}
	revs, err := c.Revisions(context.Background(), "o/m")
	// The default comes first, as every source lists them
	if err != nil || len(revs) != 3 || !revs[0].GetDefault() || revs[0].GetName() != "release" || revs[1].GetDefault() || revs[2].GetDetail() != "tag" {
		t.Fatalf("revisions %v %v", revs, err)
	}
	if _, err := c.Resolve(context.Background(), "o/bare", ""); err == nil {
		t.Fatal("a repo that names no revision should fail")
	}
}

func TestOllamaReadsEveryTagFromTheRegistry(t *testing.T) {
	manifest := func(cfg, size string) string {
		return `{"schemaVersion":2,"config":{"mediaType":"application/vnd.docker.container.image.v1+json","digest":"sha256:` + cfg + `","size":10},"layers":[{"mediaType":"application/vnd.ollama.image.model","digest":"sha256:aaaa","size":` + size + `},{"mediaType":"application/vnd.ollama.image.template","digest":"sha256:bbbb","size":10}]}`
	}
	configs := map[string]string{
		"c-latest": `{"model_format":"gguf","model_type":"3.2B","file_type":"Q4_K_M"}`,
		"c-1b":     `{"model_format":"gguf","model_type":"1.2B","file_type":"Q4_K_M"}`,
		"c-moe":    `{"model_format":"gguf","model_type":"8x7B","file_type":"Q8_0"}`,
	}
	manifests := map[string]string{"latest": manifest("c-latest", "2000"), "1b-q4_K_M": manifest("c-1b", "1000"), "mix": manifest("c-moe", "9000")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/library/x/tags":
			w.Write([]byte(`<html><a href="/library/x:latest">latest</a><a href="/library/x:1b-q4_K_M">1b</a><a href="/library/x:mix">mix</a><a href="/library/x:latest">again</a><a href="/library/other:1b">other</a></html>`))
		case strings.HasPrefix(r.URL.Path, "/v2/library/x/manifests/"):
			tag := strings.TrimPrefix(r.URL.Path, "/v2/library/x/manifests/")
			body, ok := manifests[tag]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:m-"+tag)
			w.Write([]byte(body))
		case strings.HasPrefix(r.URL.Path, "/v2/library/x/blobs/sha256:"):
			body, ok := configs[strings.TrimPrefix(r.URL.Path, "/v2/library/x/blobs/sha256:")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := testClient(t, ollama, &v1.Source{Id: "ol", Kind: v1.SourceKind_SOURCE_KIND_OLLAMA, Config: map[string]string{"endpoint": srv.URL, "registry_endpoint": srv.URL}})
	revs, err := c.Revisions(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 3 {
		t.Fatalf("revisions %v", revs)
	}
	byName := map[string]*v1.Revision{}
	for _, r := range revs {
		byName[r.GetName()] = r
	}
	if r := byName["latest"]; !r.GetDefault() || r.GetRepo() != "x:latest" || r.GetSizeBytes() != 2010 || r.GetParameters() != 3_200_000_000 || r.GetPrecision() != "Q4_K_M" || r.GetCommit() != "sha256:m-latest" || r.GetDetail() != "3.2B Q4_K_M" {
		t.Fatalf("latest %+v", r)
	}
	if r := byName["mix"]; r.GetParameters() != 56_000_000_000 || r.GetPrecision() != "Q8_0" || r.GetSizeBytes() != 9010 {
		t.Fatalf("mixture %+v", r)
	}
	// The client lists the default first, the rest by parameter count
	if revs[0].GetName() != "latest" || revs[1].GetName() != "1b-q4_K_M" || revs[2].GetName() != "mix" {
		t.Fatalf("order: %v", revs)
	}
	// The same tag list comes back from memory until it ages out
	again, _ := c.Revisions(context.Background(), "x:mix")
	if len(again) != 3 {
		t.Fatalf("cached %v", again)
	}
	model, err := c.Resolve(context.Background(), "x", "1b-q4_K_M")
	if err != nil || model.GetRepo() != "x:1b-q4_K_M" || model.GetCommit() != "sha256:m-1b-q4_K_M" || len(model.GetArtifacts()) != 2 || model.GetArtifacts()[0].GetPath() != "x-1b-q4_K_M-Q4_K_M.gguf" {
		t.Fatalf("resolve %+v %v", model, err)
	}
	for in, want := range map[string]uint64{"3.2B": 3_200_000_000, "70B": 70_000_000_000, "8x7B": 56_000_000_000, "135M": 135_000_000, "": 0, "unknown": 0, "x7B": 0} {
		if got := olParameters(in); got != want {
			t.Errorf("olParameters(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestOllamaHitsNameTheirFormat(t *testing.T) {
	c := testClient(t, ollama, &v1.Source{Id: "ol", Kind: v1.SourceKind_SOURCE_KIND_OLLAMA})
	hit := olHit(c, `<li><a href="/library/llama3.2"><h2>llama3.2</h2></a><p class="max-w-lg">Meta</p><span class="bg-indigo-50">tools</span><span class="bg-[#ddf4ff]">1b</span><span class="bg-[#ddf4ff]">3b</span><span>82.7M</span><span>&nbsp;Pulls</span></li>`)
	if hit == nil || len(hit.GetFormats()) != 1 || hit.GetFormats()[0] != "gguf" || hit.GetTask() != "tools" || hit.Extra["sizes"] != "1b,3b" || hit.GetDownloads() != 82_700_000 {
		t.Fatalf("hit %+v", hit)
	}
}
