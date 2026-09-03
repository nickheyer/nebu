package huggingface

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func server(t *testing.T, ignoreRange bool) (*httptest.Server, *[]string) {
	t.Helper()
	var auth []string
	blob := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		if r.URL.Query().Get("search") != "tiny" || r.URL.Query().Get("filter") != "gguf" {
			http.Error(w, "bad query", 400)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{{"id": "org/tiny", "downloads": 5, "likes": 1, "createdAt": "2025-01-02T03:04:05.000Z", "tags": []string{"gguf"}}})
	})
	mux.HandleFunc("/api/models/org/tiny/revision/main", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"sha": "abc123"})
	})
	mux.HandleFunc("/api/models/org/tiny/tree/main", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/models/org/tiny/tree/main?cursor=next>; rel="next"`, "http://"+r.Host))
			json.NewEncoder(w).Encode([]map[string]any{
				{"type": "file", "path": "a.gguf", "size": 36, "lfs": map[string]any{"oid": "sha_a", "size": 36}},
				{"type": "directory", "path": "sub"},
			})
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{{"type": "file", "path": "sub/README.md", "size": 3}})
	})
	mux.HandleFunc("/org/tiny/resolve/main/a.gguf", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
			return
		}
		rng := r.Header.Get("Range")
		if ignoreRange || rng == "" {
			w.Write(blob)
			return
		}
		var start, end int
		fmt.Sscanf(rng, "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(blob)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(blob[start : end+1])
	})
	return httptest.NewServer(mux), &auth
}

func TestSearchResolveOpen(t *testing.T) {
	srv, auth := server(t, false)
	defer srv.Close()
	t.Setenv("TEST_HF_TOKEN", "secret")
	src, err := New(&v1.Source{Id: "hf", Endpoint: srv.URL, TokenEnv: "TEST_HF_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := src.Search(context.Background(), "tiny", []string{"gguf"}, 5)
	if err != nil || len(hits) != 1 || hits[0].GetRepo() != "org/tiny" || hits[0].GetAuthor() != "org" || hits[0].GetUpdatedAt().AsTime().Year() != 2025 {
		t.Fatalf("hits %v err %v", hits, err)
	}
	if (*auth)[0] != "Bearer secret" {
		t.Fatalf("auth %v", *auth)
	}
	model, err := src.Resolve(context.Background(), "org/tiny", "")
	if err != nil {
		t.Fatal(err)
	}
	if model.GetCommit() != "abc123" || model.GetRevision() != "main" || len(model.GetArtifacts()) != 2 {
		t.Fatalf("model %+v", model)
	}
	if a := model.GetArtifacts()[0]; a.GetPath() != "a.gguf" || a.GetSha256() != "sha_a" || a.GetSizeBytes() != 36 {
		t.Fatalf("artifact %+v", a)
	}
	blob, err := src.Open(context.Background(), model, model.GetArtifacts()[0])
	if err != nil {
		t.Fatal(err)
	}
	defer blob.Close()
	buf := make([]byte, 10)
	if n, err := blob.ReadAt(buf, 10); err != nil || n != 10 || string(buf) != "abcdefghij" {
		t.Fatalf("ReadAt n=%d err=%v buf=%q", n, err, buf)
	}
	if n, err := blob.ReadAt(buf, 30); n != 6 || err != io.EOF || string(buf[:n]) != "uvwxyz" {
		t.Fatalf("tail n=%d err=%v buf=%q", n, err, buf[:n])
	}
	if _, err := src.Open(context.Background(), model, &v1.Artifact{Path: "x"}); err == nil {
		t.Fatal("unknown size should fail")
	}
}

func TestOpenWithoutRangeSupport(t *testing.T) {
	srv, _ := server(t, true)
	defer srv.Close()
	src, _ := New(&v1.Source{Id: "hf", Endpoint: srv.URL})
	model := &v1.Model{Repo: "org/tiny", Revision: "main"}
	blob, err := src.Open(context.Background(), model, &v1.Artifact{Path: "a.gguf", SizeBytes: 36})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if n, err := blob.ReadAt(buf, 20); err != nil || n != 5 || string(buf) != "klmno" {
		t.Fatalf("ReadAt n=%d err=%v buf=%q", n, err, buf)
	}
}

func TestErrorsSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	src, _ := New(&v1.Source{Id: "hf", Endpoint: srv.URL})
	if _, err := src.Resolve(context.Background(), "org/missing", "main"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err %v", err)
	}
}
