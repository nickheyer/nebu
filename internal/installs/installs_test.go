package installs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
)

func TestResolveAsset(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/o/r":
			w.Write([]byte(`{"default_branch":"main"}`))
		case r.URL.Path == "/repos/o/r/releases":
			json.NewEncoder(w).Encode([]map[string]any{
				{"tag_name": "b2", "assets": []map[string]any{{"name": "llama-b2-bin-win-cuda-12.4-x64.zip", "browser_download_url": srv.URL + "/dl/b2", "size": 10}}},
				{"tag_name": "b1", "assets": []map[string]any{{"name": "llama-b1-bin-ubuntu-x64.tar.gz", "browser_download_url": srv.URL + "/dl/b1", "size": 20}, {"name": "llama-b1-bin-win-cuda-12.4-x64.zip", "browser_download_url": srv.URL + "/dl/b1w", "size": 30}, {"name": "cudart-llama-bin-win-cuda-12.4-x64.zip", "browser_download_url": srv.URL + "/dl/rt", "size": 40}}},
			})
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/commits/"):
			w.Write([]byte("c0ffee"))
		case r.URL.Path == "/repos/o/r/branches" || r.URL.Path == "/repos/o/r/tags":
			w.Write([]byte("[]"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	reg, err := sources.Build([]*v1.Source{{Id: "github", Kind: v1.SourceKind_SOURCE_KIND_GITHUB, Config: map[string]string{"endpoint": srv.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Sources: reg}
	ubuntu := runtimes.PrebuiltRule{ID: "linux", Assets: []runtimes.Asset{{Prefix: "llama-b", Contains: "-bin-ubuntu-x64", Suffix: ".tar.gz"}}, Binary: "llama-server"}
	// Use the previous release when the newest lacks the asset.
	rel, err := m.resolveAssets(context.Background(), "o/r", ubuntu, "")
	if err != nil || rel.tag != "b1" || len(rel.assets) != 1 || rel.assets[0].name != "llama-b1-bin-ubuntu-x64.tar.gz" || rel.assets[0].size != 20 {
		t.Fatalf("release %+v %v", rel, err)
	}
	rel.close()
	// All assets must come from one release. The newest lacks the companion.
	cuda := runtimes.PrebuiltRule{ID: "cuda", Assets: []runtimes.Asset{{Prefix: "llama-b", Contains: "-bin-win-cuda-12.", Suffix: "-x64.zip"}, {Prefix: "cudart-llama-bin-win-cuda-12.", Suffix: "-x64.zip"}}, Binary: "llama-server.exe"}
	rel, err = m.resolveAssets(context.Background(), "o/r", cuda, "")
	if err != nil || rel.tag != "b1" || len(rel.assets) != 2 || rel.assets[1].name != "cudart-llama-bin-win-cuda-12.4-x64.zip" || rel.assets[1].size != 40 {
		t.Fatalf("two assets %+v %v", rel, err)
	}
	rel.close()
	// Explicit releases must contain every required asset.
	rel, err = m.resolveAssets(context.Background(), "o/r", cuda, "b1")
	if err != nil || rel.tag != "b1" || len(rel.assets) != 2 {
		t.Fatalf("named release %+v %v", rel, err)
	}
	rel.close()
	if _, err := m.resolveAssets(context.Background(), "o/r", cuda, "b2"); err == nil {
		t.Fatal("a named release lacking the companion should fail")
	}
	if _, err := m.resolveAssets(context.Background(), "o/r", runtimes.PrebuiltRule{Assets: []runtimes.Asset{{Prefix: "nope"}}}, ""); err == nil {
		t.Fatal("no match should fail")
	}
	none, _ := sources.Build(nil)
	if _, err := (&Manager{Sources: none}).resolveAssets(context.Background(), "o/r", cuda, ""); err == nil {
		t.Fatal("a registry without the release source should fail")
	}
	if _, err := (&Manager{}).resolveAssets(context.Background(), "o/r", cuda, ""); err == nil {
		t.Fatal("no registry should fail")
	}
}
