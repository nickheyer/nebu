package sources

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRangeBlobHonorsContentRangeOn200(t *testing.T) {
	content := []byte("abcdefghij")
	mode := "206"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		switch mode {
		case "206":
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(content[start : end+1])
		case "200-range":
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
			w.WriteHeader(http.StatusOK)
			w.Write(content[start : end+1])
		default:
			w.WriteHeader(http.StatusOK)
			w.Write(content)
		}
	}))
	defer srv.Close()
	client, _ := NewClient(srv.URL, "")
	for _, m := range []string{"206", "200-range", "200-ignored"} {
		mode = m
		blob := NewRangeBlob(client, srv.URL+"/f", int64(len(content)))
		buf := make([]byte, 3)
		if n, err := blob.ReadAt(buf, 4); n != 3 || (err != nil && err != io.EOF) || string(buf) != "efg" {
			t.Fatalf("%s: ReadAt %d %v %q", m, n, err, buf)
		}
		rc, err := RangeOf(context.Background(), blob, 7, 3)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		if string(got) != "hij" {
			t.Fatalf("%s: Range %q", m, got)
		}
	}
}
