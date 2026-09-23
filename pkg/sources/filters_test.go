package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mindex "github.com/nickheyer/nebu/pkg/mirror"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestKindFilters(t *testing.T) {
	req := &v1.SearchRequest{Filters: map[string]string{FacetKind: "language, diffusion", FacetFormat: "gguf,safetensors"}}
	kinds, err := Kinds(req)
	if err != nil || len(kinds) != 2 || kinds[0] != v1.ModelKind_MODEL_KIND_LANGUAGE || kinds[1] != v1.ModelKind_MODEL_KIND_DIFFUSION {
		t.Fatalf("kinds %v %v", kinds, err)
	}
	if KindName(v1.ModelKind_MODEL_KIND_DIFFUSION) != "diffusion" {
		t.Fatal("kind names spell the facet values")
	}
	if !AdmitsKind(req, v1.ModelKind_MODEL_KIND_LANGUAGE) || AdmitsKind(req, v1.ModelKind_MODEL_KIND_COMPONENT) {
		t.Fatal("kinds admit what they list")
	}
	if !AdmitsFormats(req, []string{"diffusion", "gguf"}) || AdmitsFormats(req, []string{"nemo"}) || AdmitsFormats(req, nil) {
		t.Fatal("formats admit what they list")
	}
	if !AdmitsKind(&v1.SearchRequest{}, v1.ModelKind_MODEL_KIND_COMPONENT) || !AdmitsFormats(&v1.SearchRequest{}, nil) {
		t.Fatal("no filter admits everything")
	}
	if _, err := Kinds(&v1.SearchRequest{Filters: map[string]string{FacetKind: "audio"}}); err == nil || !strings.Contains(err.Error(), "audio") {
		t.Fatalf("unknown kind %v", err)
	}
	if !Shared(FacetKind) {
		t.Fatal("kind is shared across providers")
	}
}

// One hub request per format tag, merged and deduplicated, with every stream's cursor carried
func TestHubSearchesEachFormatTagAndMerges(t *testing.T) {
	var mu sync.Mutex
	var filters []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/models" {
			http.NotFound(w, r)
			return
		}
		tags := r.URL.Query()["filter"]
		mu.Lock()
		filters = append(filters, strings.Join(tags, "+"))
		mu.Unlock()
		if len(tags) != 1 {
			t.Errorf("one tag per request, got %v", tags)
		}
		cursor := r.URL.Query().Get("cursor")
		switch tags[0] + "@" + cursor {
		case "gguf@":
			w.Header().Set("Link", `<http://hub/api/models?cursor=g2>; rel="next"`)
			w.Write([]byte(`[{"id":"a/shared","downloads":50,"tags":["gguf","safetensors"]},{"id":"a/gguf","downloads":10,"tags":["gguf"]}]`))
		case "gguf@g2":
			w.Write([]byte(`[{"id":"a/gguf2","downloads":1,"tags":["gguf"]}]`))
		case "safetensors@":
			w.Write([]byte(`[{"id":"a/shared","downloads":50,"tags":["gguf","safetensors"]},{"id":"a/st","downloads":30,"tags":["safetensors"]}]`))
		case "diffusers@":
			w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected request %s %s", tags[0], cursor)
			w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()
	c := testClient(t, huggingface, &v1.Source{Id: "hf", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Config: map[string]string{"endpoint": srv.URL}})
	req := &v1.SearchRequest{Sort: SortDownloads, Filters: map[string]string{FacetFormat: "gguf,diffusion,safetensors,diffusers"}}
	resp, err := c.Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var repos []string
	for _, h := range resp.GetHits() {
		repos = append(repos, h.GetRepo())
	}
	if strings.Join(repos, ",") != "a/shared,a/st,a/gguf" || resp.GetTotal() != 0 {
		t.Fatalf("merged page %v total %d", repos, resp.GetTotal())
	}
	if !strings.HasPrefix(resp.GetNextCursor(), hubUnionCursor) {
		t.Fatalf("cursor %q", resp.GetNextCursor())
	}
	req.Cursor = resp.GetNextCursor()
	resp, err = c.Search(context.Background(), req)
	if err != nil || len(resp.GetHits()) != 1 || resp.GetHits()[0].GetRepo() != "a/gguf2" || resp.GetNextCursor() != "" {
		t.Fatalf("second page %v %v", resp, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(filters) != 4 {
		t.Fatalf("three streams then one continued, got %v", filters)
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Sort: SortDownloads, Filters: map[string]string{FacetFormat: "gguf,safetensors"}, Cursor: "g2"}); err == nil || !strings.Contains(err.Error(), "not one this search issued") {
		t.Fatalf("a foreign cursor is refused: %v", err)
	}
}

// Kinds become the model types the API filters on
func TestCivitaiTypesFollowKinds(t *testing.T) {
	var calls [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Query()["types"])
		w.Write([]byte(`{"items":[],"metadata":{}}`))
	}))
	defer srv.Close()
	c := testClient(t, civitai, &v1.Source{Id: "civ", Kind: v1.SourceKind_SOURCE_KIND_CIVITAI, Config: map[string]string{"endpoint": srv.URL}})
	search := func(filters map[string]string) {
		t.Helper()
		if _, err := c.Search(context.Background(), &v1.SearchRequest{Filters: filters}); err != nil {
			t.Fatal(err)
		}
	}
	search(map[string]string{FacetKind: "language"})
	search(map[string]string{FacetKind: "diffusion", FacetType: "Checkpoint,LORA"})
	search(map[string]string{FacetKind: "language", FacetType: "Checkpoint"})
	search(nil)
	if len(calls) != 3 {
		t.Fatalf("kinds sharing no type ask the API for nothing, got %d calls", len(calls))
	}
	if strings.Join(calls[0], ",") != "VisionLanguage,LLM" || strings.Join(calls[1], ",") != "Checkpoint" || len(calls[2]) != 0 {
		t.Fatalf("types %v", calls)
	}
	if civFormat("model.safetensors", "LLM") != "safetensors" || civFormat("model.safetensors", "Checkpoint") != "diffusion" || civFormat("model.gguf", "LLM") != "gguf" {
		t.Fatal("language safetensors are the transformers layout")
	}
}

// A library of GGUF language models has nothing for other formats or kinds
func TestOllamaSkipsOtherFormats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request should reach the library, got %s", r.URL)
	}))
	defer srv.Close()
	c := testClient(t, ollama, &v1.Source{Id: "ol", Kind: v1.SourceKind_SOURCE_KIND_OLLAMA, Config: map[string]string{"endpoint": srv.URL}})
	for _, filters := range []map[string]string{{FacetFormat: "safetensors"}, {FacetKind: "diffusion"}} {
		resp, err := c.Search(context.Background(), &v1.SearchRequest{Filters: filters})
		if err != nil || len(resp.GetHits()) != 0 || resp.GetNextCursor() != "" {
			t.Fatalf("%v: %v %v", filters, resp, err)
		}
	}
}

// Directories and mirrors list by the formats their files hold
func TestLocalAndMirrorFilterByFormat(t *testing.T) {
	root := t.TempDir()
	for dir, file := range map[string]string{"a/gguf": "w.gguf", "a/hf": "model.safetensors", "b/sd": "v1-5.safetensors"} {
		os.MkdirAll(filepath.Join(root, dir), 0o755)
		os.WriteFile(filepath.Join(root, dir, file), []byte("x"), 0o644)
	}
	os.WriteFile(filepath.Join(root, "a", "hf", "config.json"), []byte("{}"), 0o644)
	c := testClient(t, local, &v1.Source{Id: "d", Config: map[string]string{"path": root}})
	list := func(c *Client, formats string) string {
		t.Helper()
		resp, err := c.Search(context.Background(), &v1.SearchRequest{Sort: SortName, Filters: map[string]string{FacetFormat: formats}})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range resp.GetHits() {
			out = append(out, h.GetRepo()+":"+strings.Join(h.GetFormats(), "+"))
		}
		return strings.Join(out, " ")
	}
	if got := list(c, ""); got != "a/gguf:gguf a/hf:safetensors b/sd:diffusion" {
		t.Fatalf("formats %q", got)
	}
	if got := list(c, "gguf,diffusion"); got != "a/gguf:gguf b/sd:diffusion" {
		t.Fatalf("filtered %q", got)
	}
	mirrorDir := t.TempDir()
	data, _ := json.Marshal(mindex.Root{Repos: []mindex.RepoEntry{{Repo: "m/gguf", Files: 1, Formats: []string{"gguf"}}, {Repo: "m/hf", Files: 2, Formats: []string{"safetensors"}}, {Repo: "m/unknown", Files: 1}}})
	os.WriteFile(filepath.Join(mirrorDir, mindex.IndexFile), data, 0o644)
	m := testClient(t, mirror, &v1.Source{Id: "m", Kind: v1.SourceKind_SOURCE_KIND_MIRROR, Config: map[string]string{"path": mirrorDir}})
	if got := list(m, ""); got != "m/gguf:gguf m/hf:safetensors m/unknown:" {
		t.Fatalf("mirror formats %q", got)
	}
	if got := list(m, "safetensors"); got != "m/hf:safetensors" {
		t.Fatalf("mirror filtered %q", got)
	}
	if got := FormatsOf([]string{"x/model-00001-of-00002.safetensors", "x/config.json", "y/unet.safetensors", "z.nemo", "q.gguf"}); strings.Join(got, ",") != "safetensors,diffusion,nemo,gguf" {
		t.Fatalf("FormatsOf %v", got)
	}
}

// Hub listings name layer media types, which say GGUF before the manifest is read
func TestDockerHubHitsNameGGUFFromMediaTypes(t *testing.T) {
	c := testClient(t, dockerhub, &v1.Source{Id: "dh", Kind: v1.SourceKind_SOURCE_KIND_OCI, Config: map[string]string{"endpoint": "http://127.0.0.1:1"}})
	hit := dhHit(c, dhItem{ID: "ai/llama", MediaTypes: []string{"application/vnd.docker.ai.gguf.v3"}})
	if strings.Join(hit.GetFormats(), ",") != "gguf" {
		t.Fatalf("formats %v", hit.GetFormats())
	}
	if hit := dhHit(c, dhItem{ID: "ai/other", MediaTypes: []string{"application/vnd.docker.ai.model.weights"}}); len(hit.GetFormats()) != 0 {
		t.Fatalf("unknown layers name no format: %v", hit.GetFormats())
	}
}
