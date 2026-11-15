// Package chats keeps the web chat's saved conversations, one store per account.
package chats

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/store"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Longest title kept, in characters
	MaxTitle = 200
	// Characters of the first message a generated title keeps
	titleChars = 60
	// Largest image stored, in bytes
	MaxFileBytes = 16 << 20
	// Characters of a conversation or file id
	maxID = 64
	// Owner of conversations made with the daemon token or with auth.disabled
	daemonProvider = "daemon"
)

var (
	// Returned when no conversation or file of the caller's has the id
	ErrUnknownConversation = errors.New("unknown conversation")
	// Returned when a request is malformed
	ErrChat = errors.New("invalid chat request")
)

// Saved conversations and their images, the bytes in the content store under their digest
type Manager struct {
	DB    *db.DB
	Blobs *store.Store
	Log   *slog.Logger
	// Overridable for tests
	Now func() time.Time
}

// The store a request reaches, the account behind its session or API token, or the
// daemon's shared store for the daemon token and auth.disabled, which name no subject
func OwnerOf(id auth.Identity) db.ChatOwner {
	if id.Subject == "" {
		return db.ChatOwner{Provider: daemonProvider}
	}
	return db.ChatOwner{Provider: id.Provider, Subject: id.Subject}
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Lists the owner's conversations without turns, most recently updated first
func (m *Manager) List(ctx context.Context, owner db.ChatOwner) ([]*v1.Conversation, error) {
	list, err := m.DB.ListConversations(ctx, owner)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []*v1.Conversation{}
	}
	return list, nil
}

// Fetches one of the owner's conversations with its turns, naming and saving any turn stored without an id
func (m *Manager) Get(ctx context.Context, owner db.ChatOwner, id string) (*v1.Conversation, error) {
	c, err := m.DB.GetConversation(ctx, owner, id)
	if db.IsNotFound(err) {
		return nil, fmt.Errorf("%w %q", ErrUnknownConversation, id)
	}
	if err != nil {
		return nil, err
	}
	named, err := nameTurns(c.GetTurns())
	if err != nil {
		return nil, err
	}
	if named {
		if _, err := m.DB.PutConversation(ctx, owner, c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Gives every turn without an id one of its own, answering whether any was given
func nameTurns(turns []*v1.ChatTurn) (bool, error) {
	named := false
	for _, t := range turns {
		if t.GetId() != "" {
			continue
		}
		var b [12]byte
		if _, err := rand.Read(b[:]); err != nil {
			return false, err
		}
		t.Id = hex.EncodeToString(b[:])
		named = true
	}
	return named, nil
}

// Removes one turn of the owner's conversation, with the tool turns answering its calls, every other turn
// staying as it is. The conversation goes with its last turn, and nil is answered for it
func (m *Manager) DeleteTurn(ctx context.Context, owner db.ChatOwner, id, turnID string) (*v1.Conversation, error) {
	return m.rewrite(ctx, owner, id, turnID, func(turns []*v1.ChatTurn, at int) ([]*v1.ChatTurn, error) {
		calls := map[string]bool{}
		for _, c := range turns[at].GetToolCalls() {
			calls[c.GetId()] = true
		}
		var kept []*v1.ChatTurn
		for i, t := range turns {
			if i == at || t.GetRole() == "tool" && calls[t.GetToolCallId()] {
				continue
			}
			kept = append(kept, t)
		}
		return kept, nil
	})
}

// Rewrites the text of one turn of the owner's conversation in place and drops every turn after it
func (m *Manager) EditTurn(ctx context.Context, owner db.ChatOwner, id, turnID, text string) (*v1.Conversation, error) {
	return m.rewrite(ctx, owner, id, turnID, func(turns []*v1.ChatTurn, at int) ([]*v1.ChatTurn, error) {
		turns[at].Text = text
		// What the old text produced no longer describes the turn
		if turns[at].GetRole() == "assistant" {
			turns[at].Error, turns[at].Stop = "", "stop"
		}
		return turns[:at+1], nil
	})
}

// Rewinds the owner's conversation for the model to answer again from a turn. A user turn stays
// as the last one, an assistant turn goes, and every turn after either goes with it, every turn
// before staying as it is. A tool turn is not asked again, the answer that called it is
func (m *Manager) RetryTurn(ctx context.Context, owner db.ChatOwner, id, turnID string) (*v1.Conversation, error) {
	return m.rewrite(ctx, owner, id, turnID, func(turns []*v1.ChatTurn, at int) ([]*v1.ChatTurn, error) {
		switch turns[at].GetRole() {
		case "user":
			return turns[:at+1], nil
		case "tool":
			return nil, fmt.Errorf("%w: turn %q is a tool's answer, ask again from the turn that called the tool", ErrChat, turnID)
		}
		if at == 0 {
			return nil, fmt.Errorf("%w: no turn comes before turn %q for the model to answer", ErrChat, turnID)
		}
		return turns[:at], nil
	})
}

// Applies a change to the turns of the owner's conversation around one of them and saves what is
// left, releasing the images only the removed turns held. A conversation left with no turn is deleted
func (m *Manager) rewrite(ctx context.Context, owner db.ChatOwner, id, turnID string, change func(turns []*v1.ChatTurn, at int) ([]*v1.ChatTurn, error)) (*v1.Conversation, error) {
	if err := checkID(turnID, "turn"); err != nil {
		return nil, err
	}
	c, err := m.Get(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	at := -1
	for i, t := range c.GetTurns() {
		if t.GetId() == turnID {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, fmt.Errorf("%w: turn %q of conversation %q", ErrUnknownConversation, turnID, id)
	}
	before := FileIDs(c)
	if c.Turns, err = change(c.GetTurns(), at); err != nil {
		return nil, err
	}
	kept := map[string]bool{}
	for _, f := range FileIDs(c) {
		kept[f] = true
	}
	var dropped []string
	for _, f := range before {
		if !kept[f] {
			dropped = append(dropped, f)
		}
	}
	var saved *v1.Conversation
	if len(c.GetTurns()) == 0 {
		if _, err := m.DB.DeleteConversation(ctx, owner, id); err != nil {
			return nil, err
		}
	} else if saved, err = m.Put(ctx, owner, c); err != nil {
		return nil, err
	}
	if len(dropped) > 0 {
		if err := m.DeleteFiles(ctx, owner, dropped); err != nil {
			m.Log.Warn("chat images not removed with their turns", "conversation", id, "err", err)
		}
	}
	return saved, nil
}

// Saves a conversation for the owner, replacing one with the same id. The
// title is taken from the first user turn when empty, a turn without an id is
// given one, and a replaced conversation keeps its creation time.
func (m *Manager) Put(ctx context.Context, owner db.ChatOwner, c *v1.Conversation) (*v1.Conversation, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: conversation is required", ErrChat)
	}
	if err := checkID(c.GetId(), "conversation"); err != nil {
		return nil, err
	}
	if len(c.GetTurns()) == 0 {
		return nil, fmt.Errorf("%w: a conversation needs at least one turn, delete it instead of emptying it", ErrChat)
	}
	seen := map[string]bool{}
	for i, t := range c.GetTurns() {
		if t.GetRole() != "user" && t.GetRole() != "assistant" && t.GetRole() != "tool" {
			return nil, fmt.Errorf("%w: turn %d has role %q, want user, assistant or tool", ErrChat, i+1, t.GetRole())
		}
		if t.GetId() != "" {
			if err := checkID(t.GetId(), "turn"); err != nil {
				return nil, err
			}
			if seen[t.GetId()] {
				return nil, fmt.Errorf("%w: turn %d repeats the id %q of an earlier turn", ErrChat, i+1, t.GetId())
			}
			seen[t.GetId()] = true
		}
		for _, f := range append(append([]*v1.ChatFile{}, t.GetImages()...), t.GetMedia()...) {
			if f.GetId() == "" && f.GetUrl() == "" && f.GetError() == "" {
				return nil, fmt.Errorf("%w: turn %d has an image with neither an id nor a url", ErrChat, i+1)
			}
			if f.GetId() != "" {
				if err := checkID(f.GetId(), "image"); err != nil {
					return nil, err
				}
			}
		}
	}
	saved := proto.Clone(c).(*v1.Conversation)
	if _, err := nameTurns(saved.GetTurns()); err != nil {
		return nil, err
	}
	saved.Title = strings.TrimSpace(saved.GetTitle())
	if saved.Title == "" {
		saved.Title = Title(saved.GetTurns())
	}
	if utf8.RuneCountInString(saved.Title) > MaxTitle {
		return nil, fmt.Errorf("%w: the title is longer than %d characters", ErrChat, MaxTitle)
	}
	saved.TurnCount = uint32(len(saved.GetTurns()))
	now := timestamppb.New(m.now())
	saved.CreatedAt, saved.UpdatedAt = now, now
	// The store keeps the first creation time on replace.
	stored, err := m.DB.PutConversation(ctx, owner, saved)
	if err != nil {
		return nil, err
	}
	if !stored {
		return nil, fmt.Errorf("%w: conversation %q belongs to another account", ErrChat, saved.GetId())
	}
	summary, err := m.DB.ConversationSummary(ctx, owner, saved.GetId())
	if err != nil {
		return nil, err
	}
	saved.CreatedAt, saved.UpdatedAt = summary.GetCreatedAt(), summary.GetUpdatedAt()
	return saved, nil
}

// Retitles one of the owner's conversations
func (m *Manager) Rename(ctx context.Context, owner db.ChatOwner, id, title string) (*v1.Conversation, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("%w: a title is required", ErrChat)
	}
	if utf8.RuneCountInString(title) > MaxTitle {
		return nil, fmt.Errorf("%w: the title is longer than %d characters", ErrChat, MaxTitle)
	}
	now := m.now()
	ok, err := m.DB.RenameConversation(ctx, owner, id, title, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownConversation, id)
	}
	c, err := m.DB.ConversationSummary(ctx, owner, id)
	if db.IsNotFound(err) {
		return nil, fmt.Errorf("%w %q", ErrUnknownConversation, id)
	}
	return c, err
}

// Removes one of the owner's conversations and the images stored for it
func (m *Manager) Delete(ctx context.Context, owner db.ChatOwner, id string) error {
	c, err := m.Get(ctx, owner, id)
	if err != nil {
		return err
	}
	ok, err := m.DB.DeleteConversation(ctx, owner, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownConversation, id)
	}
	if ids := FileIDs(c); len(ids) > 0 {
		if err := m.DeleteFiles(ctx, owner, ids); err != nil {
			m.Log.Warn("chat images not removed with their conversation", "conversation", id, "err", err)
		}
	}
	return nil
}

// Stores an image for the owner's conversations
func (m *Manager) PutFile(ctx context.Context, owner db.ChatOwner, f *v1.ChatFile, data []byte) (*v1.ChatFile, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: file is required", ErrChat)
	}
	if err := checkID(f.GetId(), "image"); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(f.GetMediaType(), "image/") && !strings.HasPrefix(f.GetMediaType(), "video/") {
		return nil, fmt.Errorf("%w: media type %q is not an image or video", ErrChat, f.GetMediaType())
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: file %s is empty", ErrChat, f.GetId())
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("%w: file %s is %d bytes, more than the %d allowed", ErrChat, f.GetId(), len(data), MaxFileBytes)
	}
	saved := proto.Clone(f).(*v1.ChatFile)
	saved.SizeBytes = uint64(len(data))
	saved.Url, saved.Error = "", ""
	digest, err := m.Blobs.PutBlob(data)
	if err != nil {
		return nil, err
	}
	stored, err := m.DB.PutChatFile(ctx, owner, saved, digest, m.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	if !stored {
		return nil, fmt.Errorf("%w: image %q belongs to another account", ErrChat, saved.GetId())
	}
	return saved, nil
}

// Fetches one of the owner's stored images with its bytes
func (m *Manager) GetFile(ctx context.Context, owner db.ChatOwner, id string) (*v1.ChatFile, []byte, error) {
	f, digest, err := m.DB.GetChatFile(ctx, owner, id)
	if db.IsNotFound(err) {
		return nil, nil, fmt.Errorf("%w: image %q", ErrUnknownConversation, id)
	}
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(m.Blobs.BlobPath(digest))
	if err != nil {
		return nil, nil, fmt.Errorf("image %s: %w", id, err)
	}
	return f, data, nil
}

// Removes stored images of the owner's, their bytes going with the last image to use them
func (m *Manager) DeleteFiles(ctx context.Context, owner db.ChatOwner, ids []string) error {
	for _, id := range ids {
		if err := checkID(id, "image"); err != nil {
			return err
		}
	}
	orphaned, err := m.DB.DeleteChatFiles(ctx, owner, ids)
	if err != nil {
		return err
	}
	for _, digest := range orphaned {
		if _, err := m.Blobs.Release(digest); err != nil {
			return err
		}
	}
	return nil
}

// Ids of every image stored for the conversation, excluding ones kept by link
func FileIDs(c *v1.Conversation) []string {
	var out []string
	for _, t := range c.GetTurns() {
		for _, f := range t.GetImages() {
			if f.GetId() != "" {
				out = append(out, f.GetId())
			}
		}
		for _, f := range t.GetMedia() {
			if f.GetId() != "" {
				out = append(out, f.GetId())
			}
		}
	}
	return out
}

// A title from the first user turn: its first line, shortened
func Title(turns []*v1.ChatTurn) string {
	for _, t := range turns {
		if t.GetRole() != "user" {
			continue
		}
		text := strings.TrimSpace(t.GetText())
		if line, _, found := strings.Cut(text, "\n"); found {
			text = strings.TrimSpace(line)
		}
		if text == "" {
			if len(t.GetImages()) > 1 {
				return fmt.Sprintf("%d images", len(t.GetImages()))
			}
			return "Image"
		}
		if utf8.RuneCountInString(text) > titleChars {
			runes := []rune(text)
			return strings.TrimSpace(string(runes[:titleChars])) + "…"
		}
		return text
	}
	return "New chat"
}

func checkID(id, what string) error {
	if id == "" {
		return fmt.Errorf("%w: %s id is required", ErrChat, what)
	}
	if len(id) > maxID {
		return fmt.Errorf("%w: %s id is longer than %d characters", ErrChat, what, maxID)
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return fmt.Errorf("%w: %s id %q has a character other than letters, digits, - and _", ErrChat, what, id)
		}
	}
	return nil
}
