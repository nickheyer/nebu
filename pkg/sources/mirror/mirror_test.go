package mirror

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/mirror"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func layout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "org", "name")
	os.MkdirAll(repo, 0o755)
	os.WriteFile(filepath.Join(repo, "m-Q4.gguf"), []byte("weights"), 0o644)
	mirror.WriteIndex(filepath.Join(repo, mirror.IndexFile), &mirror.Index{Repo: "org/name", Commit: "c1", Files: []mirror.File{{Path: "m-Q4.gguf", Size: 7, Sha256: "abc"}}})
	bare := filepath.Join(dir, "org", "bare")
	os.MkdirAll(filepath.Join(bare, "sub"), 0o755)
	os.WriteFile(filepath.Join(bare, "sub", "w.gguf"), []byte("raw"), 0o644)
	data, _ := json.Marshal(mirror.Root{Repos: []mirror.RepoEntry{{Repo: "org/name", Commit: "c1", Files: 1}}})
	os.WriteFile(filepath.Join(dir, mirror.IndexFile), data, 0o644)
	return dir
}

func TestDirectoryMirror(t *testing.T) {
	dir := layout(t)
	src, err := New(&v1.Source{Id: "m", Kind: v1.SourceKind_SOURCE_KIND_MIRROR, Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := src.Search(context.Background(), "name", nil, 10)
	if err != nil || len(hits) != 1 || hits[0].GetRepo() != "org/name" {
		t.Fatalf("search %v %v", hits, err)
	}
	model, err := src.Resolve(context.Background(), "org/name", "")
	if err != nil || model.GetCommit() != "c1" || len(model.GetArtifacts()) != 1 || model.GetArtifacts()[0].GetSha256() != "abc" {
		t.Fatalf("resolve %v %v", model, err)
	}
	blob, err := src.Open(context.Background(), model, model.GetArtifacts()[0])
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(io.NewSectionReader(blob, 0, blob.Size()))
	blob.Close()
	if string(data) != "weights" {
		t.Fatalf("blob %q", data)
	}
	bare, err := src.Resolve(context.Background(), "org/bare", "")
	if err != nil || len(bare.GetArtifacts()) != 1 || bare.GetArtifacts()[0].GetPath() != "sub/w.gguf" || bare.GetArtifacts()[0].GetSizeBytes() != 3 {
		t.Fatalf("walk fallback %v %v", bare, err)
	}
	if _, err := src.Resolve(context.Background(), "../escape", ""); err == nil {
		t.Fatal("escape should fail")
	}
	if _, err := src.Resolve(context.Background(), "org/missing", ""); err == nil {
		t.Fatal("missing repo should fail")
	}
}

func TestHTTPMirrorWithIndex(t *testing.T) {
	dir := layout(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()
	src, err := New(&v1.Source{Id: "m", Kind: v1.SourceKind_SOURCE_KIND_MIRROR, Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := src.Search(context.Background(), "", nil, 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("search %v %v", hits, err)
	}
	model, err := src.Resolve(context.Background(), "org/name", "")
	if err != nil || model.GetCommit() != "c1" {
		t.Fatalf("resolve %v %v", model, err)
	}
	blob, err := src.Open(context.Background(), model, model.GetArtifacts()[0])
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	if n, err := blob.ReadAt(buf, 2); n != 3 || (err != nil && err != io.EOF) || string(buf) != "igh" {
		t.Fatalf("range read %d %v %q", n, err, buf)
	}
}

func TestS3Listing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("list-type") != "2" {
			http.Error(w, "bad", 400)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case q.Get("delimiter") == "/" && q.Get("prefix") == "":
			io.WriteString(w, `<ListBucketResult><CommonPrefixes><Prefix>org/</Prefix></CommonPrefixes></ListBucketResult>`)
		case q.Get("delimiter") == "/":
			io.WriteString(w, `<ListBucketResult><CommonPrefixes><Prefix>org/one/</Prefix></CommonPrefixes><CommonPrefixes><Prefix>org/two/</Prefix></CommonPrefixes></ListBucketResult>`)
		case q.Get("continuation-token") == "":
			io.WriteString(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>tok</NextContinuationToken><Contents><Key>org/one/a.gguf</Key><Size>5</Size></Contents><Contents><Key>org/one/</Key><Size>0</Size></Contents></ListBucketResult>`)
		default:
			io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>org/one/b.gguf</Key><Size>6</Size></Contents></ListBucketResult>`)
		}
	}))
	defer srv.Close()
	src, err := New(&v1.Source{Id: "s3", Kind: v1.SourceKind_SOURCE_KIND_MIRROR, Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := src.Search(context.Background(), "", nil, 10)
	if err != nil || len(hits) != 2 || hits[1].GetRepo() != "org/two" {
		t.Fatalf("prefix search %v %v", hits, err)
	}
	model, err := src.Resolve(context.Background(), "org/one", "")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, a := range model.GetArtifacts() {
		paths = append(paths, a.GetPath())
	}
	if strings.Join(paths, ",") != "a.gguf,b.gguf" || model.GetArtifacts()[1].GetSizeBytes() != 6 {
		t.Fatalf("objects %v", paths)
	}
}

func TestNeedsEndpointOrPath(t *testing.T) {
	if _, err := New(&v1.Source{Id: "x", Kind: v1.SourceKind_SOURCE_KIND_MIRROR}); err == nil {
		t.Fatal("empty mirror config should fail")
	}
}
