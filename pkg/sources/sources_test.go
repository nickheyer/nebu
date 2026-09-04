package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
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

func TestBuildKeepsOrderAndSeedsEveryCatalog(t *testing.T) {
	r, err := Build(Seeds())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range r.List() {
		ids = append(ids, s.GetId())
	}
	if got := strings.Join(ids, ","); got != "huggingface,modelscope,ollama,civitai,dockerhub,kaggle,ngc,csghub" {
		t.Fatalf("seed order %s", got)
	}
	first, _ := r.Get("")
	if first.Spec().GetId() != "huggingface" || first.Spec().GetKind() != v1.SourceKind_SOURCE_KIND_HUGGINGFACE || !first.Spec().GetSeeded() {
		t.Fatalf("fallback %+v", first.Spec())
	}
	if caps := first.Capabilities(context.Background()); caps.GetEndpoint() == "" || caps.GetTokenEnv() == "" {
		t.Fatalf("seeded capabilities should carry the provider defaults %+v", caps)
	}
	for _, st := range r.Statuses(context.Background()) {
		if st.GetError() != "" || st.GetCapabilities().GetDefaultSort() == "" || st.GetCapabilities().GetRepoPattern() == "" || st.GetCapabilities().GetDescription() == "" {
			t.Fatalf("capabilities %+v", st)
		}
	}
	root := t.TempDir()
	r, err = Build(append([]*v1.Source{{Id: "disk", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root}}, Seeds()...))
	if err != nil {
		t.Fatal(err)
	}
	if first, _ := r.Get(""); first.Spec().GetId() != "disk" || len(r.List()) != 9 {
		t.Fatalf("given order %v", r.List())
	}
	if _, err := r.Get("nope"); err == nil {
		t.Fatal("unknown id should fail")
	}
	// A hub with its own endpoint or credential is a valid source now
	for _, ok := range []*v1.Source{
		{Id: "hf-mirror", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Endpoint: "https://hf-mirror.com"},
		{Id: "ghcr", Kind: v1.SourceKind_SOURCE_KIND_OCI, Endpoint: "https://ghcr.io"},
		{Id: "hf", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, TokenEnv: "OTHER_HF_TOKEN"},
	} {
		if err := Check(ok); err != nil {
			t.Fatalf("%v: %v", ok, err)
		}
	}
	for _, bad := range []*v1.Source{
		{Id: "x", Kind: v1.SourceKind_SOURCE_KIND_LOCAL},
		{Id: "m", Kind: v1.SourceKind_SOURCE_KIND_MIRROR},
		{Id: "u", Kind: v1.SourceKind_SOURCE_KIND_UNSPECIFIED},
	} {
		if err := Check(bad); err == nil || !strings.Contains(err.Error(), ErrSource.Error()) {
			t.Fatalf("%v should fail with %v, got %v", bad, ErrSource, err)
		}
	}
	for _, bad := range [][]*v1.Source{
		{{Id: "a", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root}, {Id: "a", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root}},
		{{Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root}},
		{{Id: "u", Kind: v1.SourceKind_SOURCE_KIND_UNSPECIFIED}},
	} {
		if _, err := Build(bad); err == nil {
			t.Fatalf("%v should fail", bad)
		}
	}
	// A row whose client cannot be built stays listed and says why
	r, err = Build([]*v1.Source{{Id: "m", Kind: v1.SourceKind_SOURCE_KIND_MIRROR}, Seeds()[0]})
	if err != nil {
		t.Fatal(err)
	}
	st := r.Statuses(context.Background())
	if len(st) != 2 || st[0].GetError() == "" || st[1].GetError() != "" {
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
	m := NewManager(store, nil, nil)
	if err := m.Load(ctx, []*v1.Source{{Id: " disk ", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root, Seeded: true}}); err != nil {
		t.Fatal(err)
	}
	if got := ids(m.List()); got != "disk,huggingface,modelscope,ollama,civitai,dockerhub,kaggle,ngc,csghub" {
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
	// A second start changes nothing, and a changed config entry is ignored once its row exists
	if err := m.Load(ctx, []*v1.Source{{Id: "disk", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	if disk, _ := m.Get("disk"); len(m.List()) != 9 || disk.GetPath() != root {
		t.Fatalf("second load %v", m.List())
	}
	if err := m.Load(ctx, []*v1.Source{{Id: "bad", Kind: v1.SourceKind_SOURCE_KIND_LOCAL}}); err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("invalid config entry should fail the load: %v", err)
	}

	mirror, err := m.Create(ctx, &v1.Source{Id: "hf-mirror", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Endpoint: "https://hf-mirror.com/", Options: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(m.List()); mirror.GetSeeded() || got != "disk,hf-mirror,huggingface,modelscope,ollama,civitai,dockerhub,kaggle,ngc,csghub" {
		t.Fatalf("after create %s", got)
	}
	if src, err := m.Registry.Get("hf-mirror"); err != nil || src.Capabilities(ctx).GetEndpoint() != "https://hf-mirror.com" {
		t.Fatalf("registry follows the rows: %v %v", src, err)
	}
	for _, bad := range []*v1.Source{
		{Id: "hf-mirror", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE},
		{Id: "huggingface", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root},
		{Id: "a/b", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root},
		{Id: "..", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root},
		{Id: "", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root},
		{Id: "nopath", Kind: v1.SourceKind_SOURCE_KIND_LOCAL},
		{Id: "nokind"},
	} {
		if _, err := m.Create(ctx, bad); err == nil || !strings.Contains(err.Error(), ErrSource.Error()) {
			t.Fatalf("%v should fail with %v, got %v", bad, ErrSource, err)
		}
	}
	if len(store.rows) != 10 {
		t.Fatalf("failed creates must not write, have %d rows", len(store.rows))
	}

	hf, err := m.Update(ctx, &v1.Source{Id: "huggingface", TokenEnv: "MY_HF_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if !hf.GetSeeded() || hf.GetTokenEnv() != "MY_HF_TOKEN" || hf.GetKind() != v1.SourceKind_SOURCE_KIND_HUGGINGFACE || !hf.GetUpdatedAt().AsTime().After(hf.GetCreatedAt().AsTime()) {
		t.Fatalf("update %+v", hf)
	}
	if src, _ := m.Registry.Get("huggingface"); src.Capabilities(ctx).GetTokenEnv() != "MY_HF_TOKEN" {
		t.Fatal("registry should see the new token env")
	}
	if _, err := m.Update(ctx, &v1.Source{Id: "huggingface", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Path: root}); err == nil || !strings.Contains(err.Error(), "fixed") {
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
	again := NewManager(store, nil, nil)
	if err := again.Load(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := ids(again.List()); got != "disk,huggingface,modelscope,ollama,civitai,dockerhub,kaggle,ngc,csghub" {
		t.Fatalf("reloaded order %s", got)
	}
	if hf, _ := again.Get("huggingface"); hf.GetTokenEnv() != "MY_HF_TOKEN" || !hf.GetSeeded() {
		t.Fatalf("reloaded %+v", hf)
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
