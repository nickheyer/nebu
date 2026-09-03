package installs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestResolveAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"tag_name": "b1", "assets": []map[string]any{{"name": "llama-b1-bin-win-x64.zip", "browser_download_url": "u1", "size": 10}}},
			{"tag_name": "b2", "assets": []map[string]any{{"name": "llama-b2-bin-ubuntu-x64.tar.gz", "browser_download_url": "u2", "size": 20}}},
		})
	}))
	defer srv.Close()
	m := &Manager{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a, err := m.resolveAsset(context.Background(), &v1.PrebuiltRule{Release: srv.URL, Asset: `^llama-b\d+-bin-ubuntu-x64\.tar\.gz$`})
	if err != nil || a.tag != "b2" || a.url != "u2" || a.size != 20 {
		t.Fatalf("asset %+v %v", a, err)
	}
	if _, err := m.resolveAsset(context.Background(), &v1.PrebuiltRule{Release: srv.URL, Asset: "nope"}); err == nil {
		t.Fatal("no match should fail")
	}
}
