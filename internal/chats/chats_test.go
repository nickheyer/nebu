package chats

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	d, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if len(d.Drift) != 0 {
		t.Fatalf("drift after the migrations: %v", d.Drift)
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

func TestToolTurnsSaveAndOtherRolesAreRefused(t *testing.T) {
	m := manager(t)
	ctx := context.Background()
	owner := db.ChatOwner{Provider: "local", Subject: "ann"}
	c := &v1.Conversation{Id: "c1", Turns: []*v1.ChatTurn{{Role: "user", Text: "search"}, {Role: "assistant"}, {Role: "tool", Text: "result", ToolCallId: "call1"}, {Role: "assistant", Text: "found it"}}}
	saved, err := m.Put(ctx, owner, c)
	if err != nil || len(saved.GetTurns()) != 4 || saved.GetTurns()[2].GetRole() != "tool" {
		t.Fatalf("a tool turn saves with the rest: %v %v", saved, err)
	}
	c.Turns = append(c.Turns, &v1.ChatTurn{Role: "system", Text: "be brief"})
	if _, err := m.Put(ctx, owner, c); err == nil {
		t.Fatal("a system turn is refused")
	}
}

func TestTurnsAreNamedOnSaveAndOnRead(t *testing.T) {
	m := manager(t)
	ctx := context.Background()
	owner := db.ChatOwner{Provider: "local", Subject: "ann"}
	saved, err := m.Put(ctx, owner, &v1.Conversation{Id: "c1", Turns: []*v1.ChatTurn{{Role: "user", Text: "hi", Id: "mine"}, {Role: "assistant", Text: "hello"}}})
	if err != nil || saved.GetTurns()[0].GetId() != "mine" || saved.GetTurns()[1].GetId() == "" {
		t.Fatalf("a save keeps a turn's id and names a turn without one: %v %v", saved, err)
	}
	got, err := m.Get(ctx, owner, "c1")
	if err != nil || got.GetTurns()[1].GetId() != saved.GetTurns()[1].GetId() {
		t.Fatalf("a read answers the saved ids: %v %v", got, err)
	}
	if _, err := m.DB.PutConversation(ctx, owner, &v1.Conversation{Id: "old", Title: "old", Turns: []*v1.ChatTurn{{Role: "user", Text: "before ids"}}}); err != nil {
		t.Fatal(err)
	}
	first, err := m.Get(ctx, owner, "old")
	if err != nil || first.GetTurns()[0].GetId() == "" {
		t.Fatalf("a turn stored without an id is named on read: %v %v", first, err)
	}
	again, _ := m.Get(ctx, owner, "old")
	if again.GetTurns()[0].GetId() != first.GetTurns()[0].GetId() {
		t.Fatal("the name given on read is kept")
	}
	if _, err := m.Put(ctx, owner, &v1.Conversation{Id: "c2", Turns: []*v1.ChatTurn{{Role: "user", Id: "a"}, {Role: "assistant", Id: "a"}}}); err == nil {
		t.Fatal("two turns sharing an id are refused")
	}
}

func talk(t *testing.T, m *Manager, owner db.ChatOwner) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []string{"img1", "img2"} {
		if _, err := m.PutFile(ctx, owner, &v1.ChatFile{Id: id, MediaType: "image/png"}, []byte("picture "+id)); err != nil {
			t.Fatal(err)
		}
	}
	c := &v1.Conversation{Id: "c1", Turns: []*v1.ChatTurn{
		{Id: "u1", Role: "user", Text: "search", Images: []*v1.ChatFile{{Id: "img1"}}},
		{Id: "a1", Role: "assistant", ToolCalls: []*v1.TraceToolCall{{Id: "call1", Name: "search"}}},
		{Id: "t1", Role: "tool", Text: "result", ToolCallId: "call1"},
		{Id: "a2", Role: "assistant", Text: "found it", Media: []*v1.ChatFile{{Id: "img2"}}},
		{Id: "u2", Role: "user", Text: "thanks"},
		{Id: "a3", Role: "assistant", Text: "welcome", Error: "cut short", Stop: "length"},
	}}
	if _, err := m.Put(ctx, owner, c); err != nil {
		t.Fatal(err)
	}
}

func ids(c *v1.Conversation) string {
	var out []string
	for _, t := range c.GetTurns() {
		out = append(out, t.GetId())
	}
	return strings.Join(out, " ")
}

func TestDeletingATurnLeavesTheRest(t *testing.T) {
	m := manager(t)
	ctx := context.Background()
	owner := db.ChatOwner{Provider: "local", Subject: "ann"}
	talk(t, m, owner)
	c, err := m.DeleteTurn(ctx, owner, "c1", "u2")
	if err != nil || ids(c) != "u1 a1 t1 a2 a3" || c.GetTurnCount() != 5 {
		t.Fatalf("one turn goes and the others stay in order: %q %v", ids(c), err)
	}
	c, err = m.DeleteTurn(ctx, owner, "c1", "a1")
	if err != nil || ids(c) != "u1 a2 a3" {
		t.Fatalf("a turn takes the tool turns answering its calls: %q %v", ids(c), err)
	}
	c, err = m.DeleteTurn(ctx, owner, "c1", "a2")
	if err != nil || ids(c) != "u1 a3" {
		t.Fatalf("%q %v", ids(c), err)
	}
	if _, _, err := m.GetFile(ctx, owner, "img2"); err == nil {
		t.Fatal("the image only the removed turn held is released")
	}
	if _, _, err := m.GetFile(ctx, owner, "img1"); err != nil {
		t.Fatalf("the image of a kept turn stays: %v", err)
	}
	if _, err := m.DeleteTurn(ctx, owner, "c1", "nope"); !errors.Is(err, ErrUnknownConversation) {
		t.Fatalf("an unknown turn: %v", err)
	}
	if _, err := m.DeleteTurn(ctx, db.ChatOwner{Provider: "local", Subject: "bob"}, "c1", "u1"); !errors.Is(err, ErrUnknownConversation) {
		t.Fatalf("another account's conversation: %v", err)
	}
	for _, id := range []string{"u1", "a3"} {
		if c, err = m.DeleteTurn(ctx, owner, "c1", id); err != nil {
			t.Fatal(err)
		}
	}
	if c != nil {
		t.Fatalf("the last turn answers no conversation: %v", c)
	}
	if _, err := m.Get(ctx, owner, "c1"); !errors.Is(err, ErrUnknownConversation) {
		t.Fatalf("the conversation goes with its last turn: %v", err)
	}
	if digests, _ := m.DB.ChatFileDigests(ctx); len(digests) != 0 {
		t.Fatalf("images left: %v", digests)
	}
}

func TestEditingATurnRewritesItInPlaceAndDropsWhatFollowed(t *testing.T) {
	m := manager(t)
	ctx := context.Background()
	owner := db.ChatOwner{Provider: "local", Subject: "ann"}
	talk(t, m, owner)
	c, err := m.EditTurn(ctx, owner, "c1", "a3", "you are welcome")
	if err != nil || ids(c) != "u1 a1 t1 a2 u2 a3" {
		t.Fatalf("editing the last turn drops nothing: %q %v", ids(c), err)
	}
	if last := c.GetTurns()[5]; last.GetText() != "you are welcome" || last.GetError() != "" || last.GetStop() != "stop" {
		t.Fatalf("an edited answer loses the error and stop of the text it replaced: %v", last)
	}
	c, err = m.EditTurn(ctx, owner, "c1", "u1", "look")
	if err != nil || ids(c) != "u1" || c.GetTurns()[0].GetText() != "look" || len(c.GetTurns()[0].GetImages()) != 1 {
		t.Fatalf("the turn keeps its id and images, takes the text, and ends the conversation: %v %v", c, err)
	}
	if _, _, err := m.GetFile(ctx, owner, "img2"); err == nil {
		t.Fatal("the image of a dropped turn is released")
	}
	got, _ := m.Get(ctx, owner, "c1")
	if ids(got) != "u1" || got.GetTurns()[0].GetText() != "look" {
		t.Fatalf("the edit is saved: %v", got)
	}
}

func TestRetryingRewindsToTheUserTurnTheModelAnswers(t *testing.T) {
	m := manager(t)
	ctx := context.Background()
	owner := db.ChatOwner{Provider: "local", Subject: "ann"}
	talk(t, m, owner)
	c, err := m.RetryTurn(ctx, owner, "c1", "a3")
	if err != nil || ids(c) != "u1 a1 t1 a2 u2" {
		t.Fatalf("retrying an answer drops it: %q %v", ids(c), err)
	}
	if _, err := m.RetryTurn(ctx, owner, "c1", "t1"); !errors.Is(err, ErrChat) {
		t.Fatalf("a tool turn is not asked again: %v", err)
	}
	c, err = m.RetryTurn(ctx, owner, "c1", "a2")
	if err != nil || ids(c) != "u1 a1 t1" {
		t.Fatalf("retrying the answer to a tool keeps the call and its result: %q %v", ids(c), err)
	}
	talk(t, m, owner)
	c, err = m.RetryTurn(ctx, owner, "c1", "u2")
	if err != nil || ids(c) != "u1 a1 t1 a2 u2" {
		t.Fatalf("retrying a user turn keeps it as the last: %q %v", ids(c), err)
	}
	if _, err := m.DeleteTurn(ctx, owner, "c1", "u2"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Put(ctx, owner, &v1.Conversation{Id: "c3", Turns: []*v1.ChatTurn{{Id: "u1", Role: "user", Text: "one"}, {Id: "a1", Role: "assistant", Text: "first"}, {Id: "a2", Role: "assistant", Text: "second"}}}); err != nil {
		t.Fatal(err)
	}
	c, err = m.RetryTurn(ctx, owner, "c3", "a2")
	if err != nil || ids(c) != "u1 a1" {
		t.Fatalf("retrying an answer keeps the answer a delete left before it: %q %v", ids(c), err)
	}
	if _, err := m.Put(ctx, owner, &v1.Conversation{Id: "c2", Turns: []*v1.ChatTurn{{Id: "a1", Role: "assistant", Text: "unprompted"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RetryTurn(ctx, owner, "c2", "a1"); !errors.Is(err, ErrChat) {
		t.Fatalf("an answer with no turn before it cannot be asked again: %v", err)
	}
	if got, _ := m.Get(ctx, owner, "c2"); ids(got) != "a1" {
		t.Fatalf("a refused retry changes nothing: %q", ids(got))
	}
}
