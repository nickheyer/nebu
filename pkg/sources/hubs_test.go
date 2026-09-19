package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
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
