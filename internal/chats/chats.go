// Package chats keeps the web chat's saved conversations, one store per account.
package chats

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
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

// Saved conversations and their images
type Manager struct {
	DB  *db.DB
	Log *slog.Logger
	// Overridable for tests
	Now func() time.Time
}

// The store a request reaches: the account behind its session or API token,
// or the daemon's shared store for the daemon token and auth.disabled.
func OwnerOf(sess *auth.Session) db.ChatOwner {
	if sess == nil {
		return db.ChatOwner{Provider: daemonProvider}
	}
	return db.ChatOwner{Provider: sess.Provider, Subject: sess.Subject}
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

// Fetches one of the owner's conversations with its turns
func (m *Manager) Get(ctx context.Context, owner db.ChatOwner, id string) (*v1.Conversation, error) {
	c, err := m.DB.GetConversation(ctx, owner, id)
	if db.IsNotFound(err) {
		return nil, fmt.Errorf("%w %q", ErrUnknownConversation, id)
	}
	return c, err
}

// Saves a conversation for the owner, replacing one with the same id. The
// title is taken from the first user turn when empty. A replaced conversation
// keeps its creation time.
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
	for i, t := range c.GetTurns() {
		if t.GetRole() != "user" && t.GetRole() != "assistant" {
			return nil, fmt.Errorf("%w: turn %d has role %q, want user or assistant", ErrChat, i+1, t.GetRole())
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
		if err := m.DB.DeleteChatFiles(ctx, owner, ids); err != nil {
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
	stored, err := m.DB.PutChatFile(ctx, owner, saved, data, m.now().UTC().Format(time.RFC3339Nano))
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
	f, data, err := m.DB.GetChatFile(ctx, owner, id)
	if db.IsNotFound(err) {
		return nil, nil, fmt.Errorf("%w: image %q", ErrUnknownConversation, id)
	}
	return f, data, err
}

// Removes stored images of the owner's
func (m *Manager) DeleteFiles(ctx context.Context, owner db.ChatOwner, ids []string) error {
	for _, id := range ids {
		if err := checkID(id, "image"); err != nil {
			return err
		}
	}
	return m.DB.DeleteChatFiles(ctx, owner, ids)
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
