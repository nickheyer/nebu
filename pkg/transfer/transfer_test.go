package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/nickheyer/nebu/pkg/sources"
)

func blobServer(t *testing.T, data []byte, failFirst *int32) (*httptest.Server, *int32) {
	t.Helper()
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		var start, end int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			w.Write(data)
			return
		}
		if failFirst != nil && atomic.AddInt32(failFirst, -1) >= 0 {
			w.Header().Set("Content-Length", "999999")
			w.WriteHeader(http.StatusPartialContent)
			w.Write(data[start : start+10])
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[start : end+1])
	}))
	return srv, &requests
}

func fixtureData(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i*7 + i/13)
	}
	return data
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fetcher(chunk int64) *Fetcher {
	return &Fetcher{Workers: 3, Chunk: chunk, Retries: 3, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestNewClampsAndLimits(t *testing.T) {
	f := New(0, 1, 0, 1<<20, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if f.Workers != 1 || f.Chunk != 1<<16 || f.Retries != 1 || f.Limiter == nil {
		t.Fatalf("defaults %+v", f)
	}
}

func TestFetchParallelAndVerify(t *testing.T) {
	data := fixtureData(70000)
	srv, requests := blobServer(t, data, nil)
	defer srv.Close()
	client, _ := sources.NewHTTP(srv.URL, "")
	blob := sources.NewRangeBlob(client, srv.URL+"/blob", int64(len(data)))
	partial := filepath.Join(t.TempDir(), "x.partial")
	var progress int64
	digest, err := fetcher(65536).Fetch(context.Background(), blob, partial, digestOf(data), func(d int64) { atomic.AddInt64(&progress, d) })
	if err != nil {
		t.Fatal(err)
	}
	if digest != digestOf(data) || progress != int64(len(data)) || atomic.LoadInt32(requests) != 2 {
		t.Fatalf("digest=%s progress=%d requests=%d", digest, progress, *requests)
	}
	got, _ := os.ReadFile(partial)
	if !bytes.Equal(got, data) {
		t.Fatal("content mismatch")
	}
	if _, err := os.Stat(partial + stateSuffix); !os.IsNotExist(err) {
		t.Fatal("state file should be removed after success")
	}
}

func TestFetchResumesAndRetries(t *testing.T) {
	data := fixtureData(100000)
	fail := int32(1)
	srv, requests := blobServer(t, data, &fail)
	defer srv.Close()
	client, _ := sources.NewHTTP(srv.URL, "")
	blob := sources.NewRangeBlob(client, srv.URL+"/blob", int64(len(data)))
	partial := filepath.Join(t.TempDir(), "x.partial")
	f := fetcher(10000)
	st := &state{Size: int64(len(data)), Chunk: 10000, Done: make([]bool, 10)}
	file, _ := os.Create(partial)
	file.Truncate(int64(len(data)))
	file.WriteAt(data[:10000], 0)
	file.WriteAt(data[50000:60000], 50000)
	file.Close()
	st.Done[0], st.Done[5] = true, true
	if err := saveState(partial, st); err != nil {
		t.Fatal(err)
	}
	var progress int64
	digest, err := f.Fetch(context.Background(), blob, partial, "", func(d int64) { atomic.AddInt64(&progress, d) })
	if err != nil {
		t.Fatal(err)
	}
	if digest != digestOf(data) || progress != int64(len(data)) {
		t.Fatalf("digest=%s progress=%d", digest, progress)
	}
	if n := atomic.LoadInt32(requests); n != 9 {
		t.Fatalf("expected 8 chunk requests plus one retry, got %d", n)
	}
}

func TestFetchDigestMismatch(t *testing.T) {
	data := fixtureData(5000)
	srv, _ := blobServer(t, data, nil)
	defer srv.Close()
	client, _ := sources.NewHTTP(srv.URL, "")
	blob := sources.NewRangeBlob(client, srv.URL+"/blob", int64(len(data)))
	partial := filepath.Join(t.TempDir(), "x.partial")
	_, err := fetcher(4096).Fetch(context.Background(), blob, partial, digestOf([]byte("other")), nil)
	if err == nil || !isMismatch(err) {
		t.Fatalf("expected mismatch, got %v", err)
	}
	if _, serr := os.Stat(partial); !os.IsNotExist(serr) {
		t.Fatal("partial should be removed on mismatch")
	}
}

func isMismatch(err error) bool {
	for e := err; e != nil; {
		if e == ErrDigestMismatch {
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

func TestStateRoundTrip(t *testing.T) {
	partial := filepath.Join(t.TempDir(), "x.partial")
	st := &state{Size: 10, Chunk: 4, Done: []bool{true, false, true}}
	if err := saveState(partial, st); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(partial + stateSuffix)
	var back state
	if err := json.Unmarshal(data, &back); err != nil || back.Size != 10 || !back.Done[2] {
		t.Fatalf("state %+v %v", back, err)
	}
	loaded, _ := fetcher(4).loadState(partial, 10)
	if !loaded.Done[0] || loaded.Done[1] {
		t.Fatal("state not reused")
	}
	fresh, _ := fetcher(5).loadState(partial, 10)
	if fresh.Done[0] {
		t.Fatal("chunk size change should reset state")
	}
}

func TestHashFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	data := fixtureData(3 << 20)
	os.WriteFile(path, data, 0o644)
	var seen int64
	got, err := HashFile(path, func(d int64) { seen += d })
	if err != nil || got != digestOf(data) || seen != int64(len(data)) {
		t.Fatalf("got %s err %v seen %d", got, err, seen)
	}
}
