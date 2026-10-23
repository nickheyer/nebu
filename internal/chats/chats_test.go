package chats

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/store"
)

func manager(t *testing.T) *Manager {
	t.Helper()
	blobs, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"), Upgrade(blobs))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if len(d.Drift) != 0 {
		t.Fatalf("drift after the upgrade: %v", d.Drift)
	}
	return &Manager{DB: d, Blobs: blobs, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestFilesLiveInTheContentStore(t *testing.T) {
	m := manager(t)
	ctx := context.Background()
	owner := db.ChatOwner{Provider: "local", Subject: "ann"}
	png := append([]byte{0x89, 'P', 'N', 'G'}, bytes.Repeat([]byte{7}, 100)...)
	saved, err := m.PutFile(ctx, owner, &v1.ChatFile{Id: "img1", MediaType: "image/png", Name: "a.png", Width: 4, Height: 2, Url: "x", Error: "y"}, png)
	if err != nil || saved.GetSizeBytes() != uint64(len(png)) || saved.GetUrl() != "" || saved.GetError() != "" {
		t.Fatalf("put %v %v", saved, err)
	}
	if _, err := m.PutFile(ctx, owner, &v1.ChatFile{Id: "img2", MediaType: "image/png"}, png); err != nil {
		t.Fatal(err)
	}
	f, data, err := m.GetFile(ctx, owner, "img1")
	if err != nil || !bytes.Equal(data, png) || f.GetName() != "a.png" || f.GetWidth() != 4 {
		t.Fatalf("get %v %d bytes %v", f, len(data), err)
	}
	_, digest, err := m.DB.GetChatFile(ctx, owner, "img2")
	if err != nil || !m.Blobs.HasBlob(digest) {
		t.Fatalf("bytes should be a blob: %q %v", digest, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(m.Blobs.Root(), "blobs")); len(entries) != 1 {
		t.Fatalf("two images with the same bytes share one blob, found %d", len(entries))
	}
	if err := m.DeleteFiles(ctx, owner, []string{"img1"}); err != nil || !m.Blobs.HasBlob(digest) {
		t.Fatalf("the blob stays while img2 uses it: %v", err)
	}
	if _, _, err := m.GetFile(ctx, owner, "img1"); err == nil {
		t.Fatal("img1 should be gone")
	}
	if err := m.DeleteFiles(ctx, owner, []string{"img2"}); err != nil || m.Blobs.HasBlob(digest) {
		t.Fatalf("the last image takes the blob with it: %v", err)
	}
	if _, err := m.PutFile(ctx, owner, &v1.ChatFile{Id: "doc", MediaType: "text/plain"}, png); err == nil {
		t.Fatal("only images and video are stored")
	}
}

func TestDeletingAConversationReleasesItsImages(t *testing.T) {
	m := manager(t)
	ctx := context.Background()
	owner := db.ChatOwner{Provider: "local", Subject: "ann"}
	if _, err := m.PutFile(ctx, owner, &v1.ChatFile{Id: "img1", MediaType: "image/png"}, []byte("picture")); err != nil {
		t.Fatal(err)
	}
	_, digest, _ := m.DB.GetChatFile(ctx, owner, "img1")
	c := &v1.Conversation{Id: "c1", Turns: []*v1.ChatTurn{{Role: "user", Text: "look", Images: []*v1.ChatFile{{Id: "img1"}}}, {Role: "assistant", Text: "ok"}}}
	if _, err := m.Put(ctx, owner, c); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, owner, "c1"); err != nil {
		t.Fatal(err)
	}
	if m.Blobs.HasBlob(digest) {
		t.Fatal("the conversation's image should be released")
	}
	if digests, _ := m.DB.ChatFileDigests(ctx); len(digests) != 0 {
		t.Fatalf("rows left: %v", digests)
	}
}
