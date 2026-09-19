package sources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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

// Builds a client for a catalog, the spec's settings over the catalog's defaults
func testClient(t *testing.T, cat *Catalog, spec *v1.Source) *Client {
	t.Helper()
	c, err := newClient(cat, spec, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBuildKeepsOrderAndSeedsEveryCatalog(t *testing.T) {
	r, err := Build(Seeds())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range r.List() {
		ids = append(ids, s.GetId())
	}
	if got := strings.Join(ids, ","); got != "huggingface,modelscope,ollama,civitai,dockerhub,kaggle,ngc,csghub,github" {
		t.Fatalf("seed order %s", got)
	}
	first, _ := r.Get("")
	if first.Spec().GetId() != "huggingface" || first.Spec().GetKind() != v1.SourceKind_SOURCE_KIND_HUGGINGFACE || !first.Spec().GetSeeded() || first.Spec().GetName() != "Hugging Face" {
		t.Fatalf("fallback %+v", first.Spec())
	}
	if caps := first.Capabilities(context.Background()); caps.GetEndpoint() == "" || caps.GetTokenEnv() == "" || caps.GetName() != "Hugging Face" || len(caps.GetFields()) == 0 {
		t.Fatalf("seeded capabilities should carry the provider defaults %+v", caps)
	}
	for _, st := range r.Statuses(context.Background()) {
		if st.GetError() != "" || st.GetCapabilities().GetDefaultSort() == "" || st.GetCapabilities().GetRepoPattern() == "" || st.GetCapabilities().GetDescription() == "" {
			t.Fatalf("capabilities %+v", st)
		}
	}
	if len(r.OfKind(v1.SourceKind_SOURCE_KIND_OCI)) != 1 || len(r.OfKind(v1.SourceKind_SOURCE_KIND_LOCAL)) != 0 {
		t.Fatal("sources by kind")
	}
	root := t.TempDir()
	r, err = Build(append([]*v1.Source{{Id: "disk", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": root}}}, Seeds()...))
	if err != nil {
		t.Fatal(err)
	}
	if first, _ := r.Get(""); first.Spec().GetId() != "disk" || len(r.List()) != 10 {
		t.Fatalf("given order %v", r.List())
	}
	if _, err := r.Get("nope"); err == nil {
		t.Fatal("unknown id should fail")
	}
	// A hub with its own endpoint or credential is a valid source now
	for _, ok := range []*v1.Source{
		{Id: "hf-mirror", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Config: map[string]string{"endpoint": "https://hf-mirror.com"}},
		{Id: "ghcr", Kind: v1.SourceKind_SOURCE_KIND_OCI, Config: map[string]string{"registry_endpoint": "https://ghcr.io"}},
		{Id: "hf", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Config: map[string]string{"token_env": "OTHER_HF_TOKEN", "cli_command": os.Args[0]}},
		{Id: "m", Kind: v1.SourceKind_SOURCE_KIND_MIRROR, Config: map[string]string{"endpoint": "https://mirror.example/models"}},
		{Id: "lab", Kind: v1.SourceKind_SOURCE_KIND_GIT, Config: map[string]string{"endpoint": "https://gitlab.com"}},
	} {
		if err := r.Check(ok); err != nil {
			t.Fatalf("%v: %v", ok, err)
		}
	}
	for _, bad := range []*v1.Source{
		{Id: "x", Kind: v1.SourceKind_SOURCE_KIND_LOCAL},
		{Id: "m", Kind: v1.SourceKind_SOURCE_KIND_MIRROR},
		{Id: "u", Kind: v1.SourceKind_SOURCE_KIND_UNSPECIFIED},
		{Id: "g", Kind: v1.SourceKind_SOURCE_KIND_GIT},
		{Id: "hf", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Config: map[string]string{"endpoint": "not a url"}},
		{Id: "hf", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Config: map[string]string{"token_env": "no-dashes"}},
		{Id: "hf", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Config: map[string]string{"namespace": "ai"}},
		{Id: "hf", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Config: map[string]string{"cli_command": "no-such-hf-cli"}},
	} {
		if err := r.Check(bad); err == nil || !strings.Contains(err.Error(), ErrSource.Error()) {
			t.Fatalf("%v should fail with %v, got %v", bad, ErrSource, err)
		}
	}
	for _, bad := range [][]*v1.Source{
		{{Id: "a", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": root}}, {Id: "a", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": root}}},
		{{Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": root}}},
	} {
		if _, err := Build(bad); err == nil {
			t.Fatalf("%v should fail", bad)
		}
	}
	// Broken sources remain visible with their error and editable settings.
	r, err = Build([]*v1.Source{{Id: "m", Kind: v1.SourceKind_SOURCE_KIND_MIRROR}, {Id: "u", Kind: v1.SourceKind_SOURCE_KIND_UNSPECIFIED}, Seeds()[0]})
	if err != nil {
		t.Fatal(err)
	}
	st := r.Statuses(context.Background())
	if len(st) != 3 || st[0].GetError() == "" || st[1].GetError() == "" || st[2].GetError() != "" || len(st[0].GetCapabilities().GetFields()) == 0 {
		t.Fatalf("broken status %v", st)
	}
	if src, _ := r.Get("m"); src == nil {
		t.Fatal("broken source should still resolve")
	} else if _, err := src.Search(context.Background(), &v1.SearchRequest{}); err == nil {
		t.Fatal("broken source should fail calls")
	}
	if err := r.Reload(Seeds()[:1]); err != nil || len(r.List()) != 1 {
		t.Fatalf("reload %v %v", err, r.List())
	}
}

func TestProvidersDeclareFieldsPerTransport(t *testing.T) {
	byKind := map[v1.SourceKind]*v1.Provider{}
	for _, p := range Providers() {
		byKind[p.GetKind()] = p
	}
	if len(byKind) != 12 {
		t.Fatalf("providers %d", len(byKind))
	}
	names := func(p *v1.Provider) string { return strings.Join(fieldNames(p.GetFields()), ",") }
	if got := names(byKind[v1.SourceKind_SOURCE_KIND_OCI]); got != "endpoint,registry_endpoint,registry_token_env" {
		t.Fatalf("oci fields %s", got)
	}
	if got := names(byKind[v1.SourceKind_SOURCE_KIND_HUGGINGFACE]); got != "endpoint,token_env,cli_command" {
		t.Fatalf("hub fields %s", got)
	}
	if got := names(byKind[v1.SourceKind_SOURCE_KIND_NGC]); got != "endpoint,token_env,auth_endpoint" {
		t.Fatalf("ngc fields %s", got)
	}
	local := byKind[v1.SourceKind_SOURCE_KIND_LOCAL]
	if !local.GetConfigured() || len(local.GetFields()) != 1 || !local.GetFields()[0].GetRequired() || local.GetFields()[0].GetType() != v1.ConfigType_CONFIG_TYPE_PATH || local.GetFields()[0].GetTransport() != TransportFile {
		t.Fatalf("local fields %v", local.GetFields())
	}
	kaggle := byKind[v1.SourceKind_SOURCE_KIND_KAGGLE]
	if kaggle.GetFields()[2].GetName() != "username_env" || kaggle.GetFields()[2].GetDefault() != "KAGGLE_USERNAME" || kaggle.GetName() != "Kaggle" {
		t.Fatalf("kaggle fields %v", kaggle.GetFields())
	}
	if got := strings.Join(byKind[v1.SourceKind_SOURCE_KIND_GITHUB].GetTransports(), ","); got != "http,git" {
		t.Fatalf("github transports %s", got)
	}
}

// Rows in memory, enough to drive the manager the way the database does
type memStore struct {
	rows map[string]*v1.Source
}

func (m *memStore) ListSources(context.Context) ([]*v1.Source, error) {
	var out []*v1.Source
	for _, r := range m.rows {
		out = append(out, clone(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetId() < out[j].GetId() })
	return out, nil
}

func (m *memStore) PutSource(_ context.Context, s *v1.Source) error {
	m.rows[s.GetId()] = clone(s)
	return nil
}

func (m *memStore) DeleteSource(_ context.Context, id string) (bool, error) {
	_, ok := m.rows[id]
	delete(m.rows, id)
	return ok, nil
}

func ids(list []*v1.Source) string {
	var out []string
	for _, s := range list {
		out = append(out, s.GetId())
	}
	return strings.Join(out, ",")
}

func TestManagerSeedsBootstrapsAndEdits(t *testing.T) {
	ctx := context.Background()
	store := &memStore{rows: map[string]*v1.Source{}}
	root := t.TempDir()
	m := NewManager(store, nil, nil, t.TempDir())
	if err := m.Load(ctx, []*v1.Source{{Id: " disk ", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": root}, Seeded: true}}); err != nil {
		t.Fatal(err)
	}
	seeds := "huggingface,modelscope,ollama,civitai,dockerhub,kaggle,ngc,csghub,github"
	if got := ids(m.List()); got != "disk,"+seeds {
		t.Fatalf("order %s", got)
	}
	if disk, _ := m.Get("disk"); disk.GetSeeded() || disk.GetCreatedAt() == nil {
		t.Fatalf("config entry %+v", disk)
	}
	if hf, _ := m.Get("huggingface"); !hf.GetSeeded() {
		t.Fatalf("seed %+v", hf)
	}
	if first, _ := m.Registry.Get(""); first.Spec().GetId() != "disk" {
		t.Fatal("first added source is the fallback")
	}
	// Existing rows override later bootstrap config changes.
	if err := m.Load(ctx, []*v1.Source{{Id: "disk", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": t.TempDir()}}}); err != nil {
		t.Fatal(err)
	}
	if disk, _ := m.Get("disk"); len(m.List()) != 10 || disk.GetConfig()["path"] != root {
		t.Fatalf("second load %v", m.List())
	}
	if err := m.Load(ctx, []*v1.Source{{Id: "bad", Kind: v1.SourceKind_SOURCE_KIND_LOCAL}}); err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("invalid config entry should fail the load: %v", err)
	}

	mirror, err := m.Create(ctx, &v1.Source{Id: "hf-mirror", Name: "Mirror of the Hub", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Config: map[string]string{"endpoint": "https://hf-mirror.com/", "token_env": " "}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(m.List()); mirror.GetSeeded() || mirror.GetName() != "Mirror of the Hub" || len(mirror.GetConfig()) != 1 || got != "disk,hf-mirror,"+seeds {
		t.Fatalf("after create %s %v", got, mirror)
	}
	if src, err := m.Registry.Get("hf-mirror"); err != nil || src.Capabilities(ctx).GetEndpoint() != "https://hf-mirror.com" {
		t.Fatalf("registry follows the rows: %v %v", src, err)
	}
	for _, bad := range []*v1.Source{
		{Id: "hf-mirror", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE},
		{Id: "huggingface", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": root}},
		{Id: "a/b", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": root}},
		{Id: "..", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": root}},
		{Id: "", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": root}},
		{Id: "nopath", Kind: v1.SourceKind_SOURCE_KIND_LOCAL},
		{Id: "typo", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": root, "paht": root}},
		{Id: "nokind"},
	} {
		if _, err := m.Create(ctx, bad); err == nil || !strings.Contains(err.Error(), ErrSource.Error()) {
			t.Fatalf("%v should fail with %v, got %v", bad, ErrSource, err)
		}
	}
	if len(store.rows) != 11 {
		t.Fatalf("failed creates must not write, have %d rows", len(store.rows))
	}

	hf, err := m.Update(ctx, &v1.Source{Id: "huggingface", Name: "The Hub", Config: map[string]string{"token_env": "MY_HF_TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	if !hf.GetSeeded() || hf.GetName() != "The Hub" || hf.GetConfig()["token_env"] != "MY_HF_TOKEN" || hf.GetKind() != v1.SourceKind_SOURCE_KIND_HUGGINGFACE || !hf.GetUpdatedAt().AsTime().After(hf.GetCreatedAt().AsTime()) {
		t.Fatalf("update %+v", hf)
	}
	if src, _ := m.Registry.Get("huggingface"); src.Capabilities(ctx).GetTokenEnv() != "MY_HF_TOKEN" || src.Capabilities(ctx).GetEndpoint() != "https://huggingface.co" {
		t.Fatal("registry should see the new token env over the default endpoint")
	}
	if _, err := m.Update(ctx, &v1.Source{Id: "huggingface", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": root}}); err == nil || !strings.Contains(err.Error(), "fixed") {
		t.Fatalf("kind change %v", err)
	}
	if _, err := m.Update(ctx, &v1.Source{Id: "disk", Kind: v1.SourceKind_SOURCE_KIND_LOCAL}); err == nil || !strings.Contains(err.Error(), ErrSource.Error()) {
		t.Fatalf("invalid update %v", err)
	}
	if _, err := m.Update(ctx, &v1.Source{Id: "nope"}); err == nil || !strings.Contains(err.Error(), ErrUnknownSource.Error()) {
		t.Fatalf("unknown update %v", err)
	}

	if _, err := m.Delete(ctx, "huggingface"); err == nil || !strings.Contains(err.Error(), "seeded") {
		t.Fatalf("seeded delete %v", err)
	}
	if _, err := m.Delete(ctx, "nope"); err == nil || !strings.Contains(err.Error(), ErrUnknownSource.Error()) {
		t.Fatalf("unknown delete %v", err)
	}
	if gone, err := m.Delete(ctx, "hf-mirror"); err != nil || gone.GetId() != "hf-mirror" {
		t.Fatalf("delete %v %v", gone, err)
	}
	if _, err := m.Registry.Get("hf-mirror"); err == nil {
		t.Fatal("deleted source should leave the registry")
	}

	// Everything survives a restart through the store
	again := NewManager(store, nil, nil, t.TempDir())
	if err := again.Load(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := ids(again.List()); got != "disk,"+seeds {
		t.Fatalf("reloaded order %s", got)
	}
	if hf, _ := again.Get("huggingface"); hf.GetConfig()["token_env"] != "MY_HF_TOKEN" || !hf.GetSeeded() {
		t.Fatalf("reloaded %+v", hf)
	}
}

func TestClientChecksSortsAndStampsIds(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "org", "model"), 0o755)
	os.WriteFile(filepath.Join(root, "org", "model", "weights.gguf"), []byte("x"), 0o644)
	c := testClient(t, local, &v1.Source{Id: "d", Config: map[string]string{"path": root}})
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Sort: "bogus"}); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown sort %v", err)
	}
	resp, err := c.Search(context.Background(), &v1.SearchRequest{Sort: SortName, Ascending: true})
	if err != nil || len(resp.GetHits()) != 1 || resp.GetHits()[0].GetRepo() != "org/model" || resp.GetHits()[0].GetSourceId() != "d" {
		t.Fatalf("search %v %v", resp, err)
	}
	model, err := c.Resolve(context.Background(), "org/model", "")
	if err != nil || len(model.GetArtifacts()) != 1 || model.GetArtifacts()[0].GetPath() != "weights.gguf" || model.GetSourceId() != "d" {
		t.Fatalf("resolve %v %v", model, err)
	}
	if _, err := c.Resolve(context.Background(), "../../etc", ""); err == nil {
		t.Fatal("escaping repo should fail")
	}
	b, err := c.Open(context.Background(), model, model.GetArtifacts()[0])
	if err != nil {
		t.Fatal(err)
	}
	if path, _ := b.(Materializer).Materialize(context.Background(), nil); path != filepath.Join(root, "org", "model", "weights.gguf") {
		t.Fatalf("local blob path %s", path)
	}
	b.Close()
	if _, err := c.Revisions(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), ErrUnsupported.Error()) {
		t.Fatalf("revisions %v", err)
	}
	if _, err := c.Card(context.Background(), "x", ""); err == nil || !strings.Contains(err.Error(), ErrUnsupported.Error()) {
		t.Fatalf("card %v", err)
	}
	caps := c.Capabilities(context.Background())
	if caps.GetCard() || caps.GetRevisions() || !caps.GetBrowse() || caps.GetWebUrl() != root || len(caps.GetSorts()) != 2 || !caps.GetSorts()[0].GetReversible() {
		t.Fatalf("caps %+v", caps)
	}
	c = testClient(t, huggingface, &v1.Source{Id: "hf", Config: map[string]string{"endpoint": notFoundServer(t).URL}})
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Ascending: true}); err == nil || !strings.Contains(err.Error(), "ascending") {
		t.Fatalf("ascending %v", err)
	}
	for _, s := range c.Capabilities(context.Background()).GetSorts() {
		if s.GetReversible() {
			t.Fatalf("hub sort %s should not be reversible", s.GetId())
		}
	}
	if c.CLI() != nil || c.HTTP() == nil || c.Git() != nil {
		t.Fatal("transports the source did not turn on stay nil")
	}
}

func TestGitTransportListsTreesAndPointers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := filepath.Join(t.TempDir(), "org", "model")
	os.MkdirAll(repo, 0o755)
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "uploadpack.allowFilter", "true")
	run("config", "uploadpack.allowAnySHA1InWant", "true")
	os.WriteFile(filepath.Join(repo, "config.json"), []byte(`{"a":1}`), 0o644)
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("ab", 32) + "\nsize 123456789\n"
	os.WriteFile(filepath.Join(repo, "model.safetensors"), []byte(pointer), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "one")
	run("tag", "v1")
	c := testClient(t, gitrepo, &v1.Source{Id: "g", Config: map[string]string{"endpoint": "file://" + filepath.Dir(filepath.Dir(repo))}})
	revs, err := c.Revisions(context.Background(), "org/model")
	if err != nil || len(revs) != 2 || revs[0].GetName() != "main" || !revs[0].GetDefault() || revs[1].GetName() != "v1" || revs[1].GetDetail() != "tag" {
		t.Fatalf("revisions %v %v", revs, err)
	}
	model, err := c.Resolve(context.Background(), "org/model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if model.GetRevision() != "v1" || len(model.GetCommit()) != 40 || len(model.GetArtifacts()) != 2 {
		t.Fatalf("model %v", model)
	}
	weights := model.GetArtifacts()[1]
	if model.GetArtifacts()[0].GetPath() != "config.json" || model.GetArtifacts()[0].GetSizeBytes() != 7 || weights.GetPath() != "model.safetensors" || weights.GetSizeBytes() != 123456789 || weights.GetSha256() != strings.Repeat("ab", 32) {
		t.Fatalf("artifacts %v", model.GetArtifacts())
	}
	b, err := c.Open(context.Background(), model, model.GetArtifacts()[0])
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 7)
	if _, err := b.ReadAt(buf, 0); err != nil || string(buf) != `{"a":1}` {
		t.Fatalf("plain blob %q %v", buf, err)
	}
	b.Close()
	if card, err := c.Card(context.Background(), "org/model", ""); err != nil || card.GetMarkdown() != "" || card.GetUrl() == "" {
		t.Fatalf("card %v %v", card, err)
	}
	if _, err := c.Resolve(context.Background(), "org/model", "nope"); err == nil {
		t.Fatal("unknown ref should fail")
	}
}

func TestGitHubResolvesReleasesAndRefs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r":
			w.Write([]byte(`{"default_branch":"main"}`))
		case "/repos/o/r/releases":
			w.Write([]byte(`[{"tag_name":"v2-rc","prerelease":true,"assets":[{"name":"bin-rc.tar.gz","size":5,"browser_download_url":"` + "http://" + r.Host + `/dl/rc"}]},{"tag_name":"v1","assets":[{"name":"bin-linux.tar.gz","size":3,"browser_download_url":"` + "http://" + r.Host + `/dl/v1","digest":"sha256:abc"}]}]`))
		case "/repos/o/r/commits/v1", "/repos/o/r/commits/v2-rc":
			w.Write([]byte("deadbeef"))
		case "/repos/o/r/branches":
			w.Write([]byte(`[{"name":"main","commit":{"sha":"cafe"}}]`))
		case "/repos/o/r/tags":
			w.Write([]byte(`[{"name":"v1","commit":{"sha":"deadbeef"}},{"name":"old","commit":{"sha":"0ld"}}]`))
		case "/repos/o/r/readme":
			w.Write([]byte("# hello"))
		case "/search/repositories":
			if r.URL.Query().Get("q") != "llama" && r.URL.Query().Get("q") != "stars:>0" {
				http.Error(w, r.URL.Query().Get("q"), 400)
				return
			}
			w.Write([]byte(`{"total_count":1,"items":[{"full_name":"o/r","name":"r","owner":{"login":"o"},"stargazers_count":9,"html_url":"u","license":{"spdx_id":"MIT"}}]}`))
		case "/dl/v1":
			w.Header().Set("Content-Range", "bytes 0-2/3")
			w.WriteHeader(206)
			w.Write([]byte("abc"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := testClient(t, github, &v1.Source{Id: "gh", Config: map[string]string{"endpoint": srv.URL}})
	revs, err := c.Revisions(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range revs {
		names = append(names, r.GetName()+":"+r.GetDetail())
	}
	if got := strings.Join(names, ","); got != "v1:release,v2-rc:prerelease,main:branch,old:tag" || !revs[0].GetDefault() || revs[0].GetSizeBytes() != 3 {
		t.Fatalf("revisions %s %v", got, revs)
	}
	model, err := c.Resolve(context.Background(), "o/r", "")
	if err != nil || model.GetRevision() != "v1" || model.GetCommit() != "deadbeef" || len(model.GetArtifacts()) != 1 || model.GetArtifacts()[0].GetSha256() != "abc" {
		t.Fatalf("latest release %v %v", model, err)
	}
	b, err := c.Open(context.Background(), model, model.GetArtifacts()[0])
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	if _, err := b.ReadAt(buf, 0); err != nil || string(buf) != "abc" {
		t.Fatalf("asset %q %v", buf, err)
	}
	if card, err := c.Card(context.Background(), "o/r", ""); err != nil || card.GetMarkdown() != "# hello" {
		t.Fatalf("card %v %v", card, err)
	}
	hits, err := c.Search(context.Background(), &v1.SearchRequest{Query: "llama"})
	if err != nil || len(hits.GetHits()) != 1 || hits.GetHits()[0].GetLikes() != 9 || hits.GetHits()[0].GetLicense() != "MIT" {
		t.Fatalf("search %v %v", hits, err)
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{}); err != nil {
		t.Fatalf("browse %v", err)
	}
}

func TestGitHubListsOnePageOfEachKind(t *testing.T) {
	const pages = 12
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.RequestURI())
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		switch r.URL.Path {
		case "/repos/o/r":
			w.Write([]byte(`{"default_branch":"main"}`))
		case "/repos/o/r/releases":
			w.Header().Set("Link", fmt.Sprintf(`<http://%s/repos/o/r/releases?per_page=%d&page=%d>; rel="next"`, r.Host, ghPageSize, page+1))
			var items []string
			for i := 0; i < ghPageSize; i++ {
				n := (pages-page)*ghPageSize + ghPageSize - i
				items = append(items, fmt.Sprintf(`{"tag_name":"b%d","prerelease":%v,"assets":[{"name":"bin-b%d.tar.gz","size":1,"browser_download_url":"http://%s/dl/b%d"}]}`, n, page == 1, n, r.Host, n))
			}
			w.Write([]byte("[" + strings.Join(items, ",") + "]"))
		case "/repos/o/r/releases/latest":
			w.Write([]byte(`{"tag_name":"b1100","assets":[{"name":"bin-b1100.tar.gz","size":1,"browser_download_url":"http://` + r.Host + `/dl/b1100"}]}`))
		case "/repos/o/r/releases/tags/b7":
			w.Write([]byte(`{"tag_name":"b7","assets":[{"name":"bin-b7.tar.gz","size":1,"browser_download_url":"http://` + r.Host + `/dl/b7"}]}`))
		case "/repos/o/r/commits/b1100", "/repos/o/r/commits/b7":
			w.Write([]byte("deadbeef"))
		case "/repos/o/r/commits/main":
			w.Write([]byte("cafe"))
		case "/repos/o/r/branches":
			// A full page of branches sorting before the default one, with more beyond
			w.Header().Set("Link", fmt.Sprintf(`<http://%s/repos/o/r/branches?per_page=%d&page=%d>; rel="next"`, r.Host, ghPageSize, page+1))
			var items []string
			for i := 0; i < ghPageSize; i++ {
				items = append(items, fmt.Sprintf(`{"name":"a%02d","commit":{"sha":"%02d"}}`, i, i))
			}
			w.Write([]byte("[" + strings.Join(items, ",") + "]"))
		case "/repos/o/r/tags":
			w.Write([]byte(`[{"name":"b1200","commit":{"sha":"1200"}},{"name":"v0","commit":{"sha":"0"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := testClient(t, github, &v1.Source{Id: "gh", Config: map[string]string{"endpoint": srv.URL}})

	// Expected requests: releases, stable release, default branch, branches, and a non-release tag.
	model, err := c.Resolve(context.Background(), "o/r", "")
	if err != nil || model.GetRevision() != "b1100" || len(model.GetArtifacts()) != 1 {
		t.Fatalf("latest %v %v", model, err)
	}
	// A tag far past the page still resolves to its assets rather than a tree
	model, err = c.Resolve(context.Background(), "o/r", "b7")
	if err != nil || len(model.GetArtifacts()) != 1 || model.GetArtifacts()[0].GetPath() != "bin-b7.tar.gz" {
		t.Fatalf("old release %v %v", model, err)
	}
	revs, err := c.Revisions(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	// Expected requests: releases, stable release, default branch, branches, and a non-release tag.
	if len(revs) != 2*ghPageSize+3 || !revs[0].GetDefault() || revs[0].GetName() != "b1100" || revs[0].GetDetail() != "release" {
		t.Fatalf("revisions %d %v", len(revs), revs[0])
	}
	branches := ghPageSize + 1
	if b := revs[branches]; b.GetName() != "main" || b.GetCommit() != "cafe" || b.GetDetail() != "branch" || b.GetDefault() {
		t.Fatalf("default branch %v", b)
	}
	if b := revs[branches+1]; b.GetName() != "a00" || b.GetDetail() != "branch" {
		t.Fatalf("first listed branch %v", b)
	}
	if last := revs[len(revs)-1]; last.GetName() != "v0" || last.GetDetail() != "tag" {
		t.Fatalf("tag %v", last)
	}
	listed := len(hits)
	if _, err := c.Revisions(context.Background(), "o/r"); err != nil || len(hits) != listed {
		t.Fatalf("listing again asked the API %v %v", hits[listed:], err)
	}
	counts := map[string]int{}
	for _, h := range hits {
		if strings.Contains(h, "&page=") {
			t.Fatalf("read a second page: %s", h)
		}
		counts[strings.SplitN(h, "?", 2)[0]]++
	}
	for _, path := range []string{"/repos/o/r/releases", "/repos/o/r/branches", "/repos/o/r/tags", "/repos/o/r/commits/main"} {
		if counts[path] != 1 {
			t.Fatalf("%s read %d times: %v", path, counts[path], hits)
		}
	}
}
