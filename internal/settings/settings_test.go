package settings

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	m := &Manager{DB: store}
	if err := m.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return m
}

// Web search settings are trimmed and kept, and a provider missing what it needs is refused
func TestWebSearchSettings(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	for _, bad := range []*v1.WebSearch{
		{Provider: "searxng"},
		{Provider: "searxng", SearxngUrl: "searx.local"},
		{Provider: "searxng", SearxngUrl: "ftp://searx.local"},
		{Provider: "brave"},
		{Provider: "brave", BraveKey: "   "},
		{Provider: "google", BraveKey: "k"},
	} {
		if _, err := m.Update(ctx, &v1.Settings{WebSearch: bad}); !errors.Is(err, ErrSetting) {
			t.Fatalf("%v: %v", bad, err)
		}
	}
	got, err := m.Update(ctx, &v1.Settings{HostLabel: "box", WebSearch: &v1.WebSearch{Provider: " searxng ", SearxngUrl: " http://searx.local:8080/ "}})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetWebSearch().GetProvider() != "searxng" || got.GetWebSearch().GetSearxngUrl() != "http://searx.local:8080" {
		t.Fatalf("stored %v", got.GetWebSearch())
	}
	again := &Manager{DB: m.DB}
	if err := again.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if again.Get().GetWebSearch().GetSearxngUrl() != "http://searx.local:8080" || again.Get().GetHostLabel() != "box" {
		t.Fatalf("loaded %v", again.Get())
	}
	if got, err := m.Update(ctx, &v1.Settings{WebSearch: &v1.WebSearch{Provider: "brave", BraveKey: " tok "}}); err != nil || got.GetWebSearch().GetBraveKey() != "tok" {
		t.Fatalf("brave: %v %v", got, err)
	}
	if got, err := m.Update(ctx, &v1.Settings{WebSearch: &v1.WebSearch{SearxngUrl: "left over"}}); err != nil || got.GetWebSearch().GetProvider() != "" {
		t.Fatalf("no provider keeps the rest: %v %v", got, err)
	}
}
