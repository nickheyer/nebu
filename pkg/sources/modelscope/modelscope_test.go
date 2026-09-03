package modelscope

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func hub(t *testing.T, sameRevision bool) *httptest.Server {
	t.Helper()
	content := []byte("0123456789abcdef")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/dolphin/models":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["Name"] != "qwen" || r.Header.Get("Authorization") != "Bearer tok" {
				http.Error(w, "bad search", 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"Code": 200, "Data": map[string]any{"Model": map[string]any{"Models": []map[string]any{
				{"Path": "unsloth", "Name": "Qwen-GGUF", "Downloads": 12, "Stars": 3, "LastUpdatedTime": 1700000000, "Tags": []string{"gguf"}},
				{"Path": "", "Name": "skipped"},
			}}}})
		case r.URL.Path == "/api/v1/models/Qwen/Qwen3-0.6B-GGUF/repo/files":
			rev2 := "aaaa"
			if sameRevision {
				rev2 = "6abe"
			}
			json.NewEncoder(w).Encode(map[string]any{"Code": 200, "Message": "success", "Data": map[string]any{"Files": []map[string]any{
				{"Type": "blob", "Path": "Qwen3-0.6B-Q8_0.gguf", "Size": 16, "Sha256": "ABCD", "Revision": "6abe"},
				{"Type": "tree", "Path": "dir", "Size": 0},
				{"Type": "blob", "Path": "README.md", "Size": 3, "Sha256": "ef", "Revision": rev2},
			}}})
		case r.URL.Path == "/api/v1/models/Qwen/Qwen3-0.6B-GGUF/repo":
			if r.URL.Query().Get("FilePath") != "Qwen3-0.6B-Q8_0.gguf" {
				http.NotFound(w, r)
				return
			}
			var start, end int
			fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
			w.WriteHeader(http.StatusOK)
			w.Write(content[start : end+1])
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestSearchResolveOpen(t *testing.T) {
	srv := hub(t, true)
	defer srv.Close()
	t.Setenv("MS_TOKEN", "tok")
	src, err := New(&v1.Source{Id: "ms", Kind: v1.SourceKind_SOURCE_KIND_MODELSCOPE, Endpoint: srv.URL, TokenEnv: "MS_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := src.Search(context.Background(), "qwen", []string{"gguf"}, 5)
	if err != nil || len(hits) != 1 || hits[0].GetRepo() != "unsloth/Qwen-GGUF" || hits[0].GetDownloads() != 12 || hits[0].GetLikes() != 3 || hits[0].GetUpdatedAt() == nil || hits[0].GetTags()[0] != "gguf" {
		t.Fatalf("search %v %v", hits, err)
	}
	model, err := src.Resolve(context.Background(), "Qwen/Qwen3-0.6B-GGUF", "")
	if err != nil {
		t.Fatal(err)
	}
	if model.GetRevision() != "master" || model.GetCommit() != "6abe" || len(model.GetArtifacts()) != 2 || model.GetArtifacts()[0].GetSha256() != "abcd" {
		t.Fatalf("resolve %v", model)
	}
	blob, err := src.Open(context.Background(), model, model.GetArtifacts()[0])
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if n, err := blob.ReadAt(buf, 6); n != 4 || (err != nil && err != io.EOF) || string(buf) != "6789" {
		t.Fatalf("200 with content-range must not skip: %d %v %q", n, err, buf)
	}
	rc, err := blob.(interface {
		Range(context.Context, int64, int64) (io.ReadCloser, error)
	}).Range(context.Background(), 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "abc" {
		t.Fatalf("range %q", got)
	}
}

func TestMixedRevisionsHashTree(t *testing.T) {
	srv := hub(t, false)
	defer srv.Close()
	src, _ := New(&v1.Source{Id: "ms", Kind: v1.SourceKind_SOURCE_KIND_MODELSCOPE, Endpoint: srv.URL})
	model, err := src.Resolve(context.Background(), "Qwen/Qwen3-0.6B-GGUF", "v2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(model.GetCommit(), "tree-") || model.GetRevision() != "v2" {
		t.Fatalf("commit %q revision %q", model.GetCommit(), model.GetRevision())
	}
}
