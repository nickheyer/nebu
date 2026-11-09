package transfer

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/sources"
)

// Serves ranges in paced pieces, the first request for the range at lagAt handled by lag instead
func laggingServer(t *testing.T, data []byte, lagAt int, lag func(w http.ResponseWriter, r *http.Request, body []byte)) (*httptest.Server, *[]int) {
	t.Helper()
	var mu sync.Mutex
	var starts []int
	lagged := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			http.Error(w, "range required", http.StatusBadRequest)
			return
		}
		mu.Lock()
		starts = append(starts, start)
		first := start == lagAt && !lagged
		if first {
			lagged = true
		}
		mu.Unlock()
		body := data[start : end+1]
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(http.StatusPartialContent)
		if first {
			lag(w, r, body)
			return
		}
		piece := (len(body) + 3) / 4
		for len(body) > 0 {
			n := min(piece, len(body))
			w.Write(body[:n])
			w.(http.Flusher).Flush()
			body = body[n:]
			time.Sleep(30 * time.Millisecond)
		}
	}))
	return srv, &starts
}

// Fetches data through a lagging server and returns the notes, the range starts asked for, and the time taken
func fetchLagging(t *testing.T, data []byte, chunk int64, lagAt int, lag func(w http.ResponseWriter, r *http.Request, body []byte)) ([]string, []int, time.Duration) {
	t.Helper()
	srv, starts := laggingServer(t, data, lagAt, lag)
	defer srv.Close()
	client, _ := sources.NewHTTP(srv.URL, "")
	blob := sources.NewRangeBlob(context.Background(), client, srv.URL+"/blob", int64(len(data)))
	partial := filepath.Join(t.TempDir(), "x.partial")
	var mu sync.Mutex
	var notes []string
	ctx := WithNotes(context.Background(), func(line string) {
		mu.Lock()
		notes = append(notes, line)
		mu.Unlock()
	})
	var progress int64
	began := time.Now()
	f := fetcher(chunk)
	f.Window = 200 * time.Millisecond
	digest, err := f.Fetch(ctx, blob, partial, digestOf(data), func(d int64) { atomic.AddInt64(&progress, d) })
	took := time.Since(began)
	if err != nil {
		t.Fatal(err)
	}
	if digest != digestOf(data) || atomic.LoadInt64(&progress) != int64(len(data)) {
		t.Fatalf("digest=%s progress=%d of %d", digest, progress, len(data))
	}
	got, _ := os.ReadFile(partial)
	if !bytes.Equal(got, data) {
		t.Fatal("content mismatch")
	}
	return notes, *starts, took
}

func TestFetchRestartsSlowStream(t *testing.T) {
	data := fixtureData(6 * 65536)
	lagAt := 2 * 65536
	// Sixteen bytes every twenty milliseconds, a chunk of it taking over a minute
	trickle := func(w http.ResponseWriter, r *http.Request, body []byte) {
		for len(body) > 0 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
			w.Write(body[:16])
			w.(http.Flusher).Flush()
			body = body[16:]
		}
	}
	notes, starts, took := fetchLagging(t, data, 65536, lagAt, trickle)
	if took > 10*time.Second {
		t.Fatalf("fetch took %s, the slow stream was never restarted", took)
	}
	if len(starts) != 7 {
		t.Fatalf("expected 6 chunk requests plus one restart, got %d: %v", len(starts), starts)
	}
	resumed := false
	for _, s := range starts {
		if s > lagAt && s < lagAt+65536 {
			resumed = true
		}
	}
	if !resumed {
		t.Fatalf("the restart did not take up the chunk where the slow stream left it: %v", starts)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "fell to") || !strings.Contains(notes[0], "restarted") {
		t.Fatalf("notes=%q", notes)
	}
}

func TestFetchRestartsStalledStream(t *testing.T) {
	const chunk = 1 << 20
	data := fixtureData(4 * chunk)
	lagAt := chunk
	// Three quarters of the chunk at once, then silence on an open connection
	const sent = 12 * 65536
	stall := func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Write(body[:sent])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}
	notes, starts, took := fetchLagging(t, data, chunk, lagAt, stall)
	if took > 10*time.Second {
		t.Fatalf("fetch took %s, the stalled stream was never restarted", took)
	}
	if len(starts) != 5 || starts[len(starts)-1] != lagAt+sent {
		t.Fatalf("expected 4 chunk requests and a restart at %d, got %v", lagAt+sent, starts)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "nothing arrived") || !strings.Contains(notes[0], "restarted") {
		t.Fatalf("notes=%q", notes)
	}
}

func TestFetchFailsStreamThatNeverDelivers(t *testing.T) {
	data := fixtureData(65536)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	client, _ := sources.NewHTTP(srv.URL, "")
	blob := sources.NewRangeBlob(context.Background(), client, srv.URL+"/blob", int64(len(data)))
	f := fetcher(65536)
	f.Retries = 2
	f.Window = 100 * time.Millisecond
	_, err := f.Fetch(context.Background(), blob, filepath.Join(t.TempDir(), "x.partial"), "", nil)
	if err == nil || !strings.Contains(err.Error(), "stream stalled") {
		t.Fatalf("err=%v", err)
	}
}
