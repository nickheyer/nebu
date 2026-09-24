package db

import (
	"context"
	"database/sql"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// The web chat convo's user account
type ChatOwner struct {
	Provider, Subject string
}

const conversationColumns = `id, title, model, turn_count, created_at, updated_at`

// Inserts or replaces one of the owner's conversations
func (d *DB) PutConversation(ctx context.Context, owner ChatOwner, c *v1.Conversation) (bool, error) {
	body, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(&v1.Conversation{Settings: c.GetSettings(), Turns: c.GetTurns()})
	if err != nil {
		return false, err
	}
	res, err := d.sql.ExecContext(ctx, `INSERT INTO conversations (id, provider, subject, title, model, turn_count, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET title = excluded.title, model = excluded.model, turn_count = excluded.turn_count, body = excluded.body, updated_at = excluded.updated_at
		WHERE conversations.provider = excluded.provider AND conversations.subject = excluded.subject`,
		c.GetId(), owner.Provider, owner.Subject, c.GetTitle(), c.GetModel(), len(c.GetTurns()), string(body), stamp(c.GetCreatedAt().AsTime()), stamp(c.GetUpdatedAt().AsTime()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Lists the owner's conversations without turns, most recently updated first
func (d *DB) ListConversations(ctx context.Context, owner ChatOwner) ([]*v1.Conversation, error) {
	return list(ctx, d, `SELECT `+conversationColumns+` FROM conversations WHERE provider = ? AND subject = ? ORDER BY updated_at DESC, id`, scanConversation, owner.Provider, owner.Subject)
}

func scanConversation(rows *sql.Rows) (*v1.Conversation, error) {
	c := &v1.Conversation{}
	return c, rows.Scan(&c.Id, &c.Title, &c.Model, &c.TurnCount, at{&c.CreatedAt}, at{&c.UpdatedAt})
}

// Fetches one of the owner's conversations without its turns and settings
func (d *DB) ConversationSummary(ctx context.Context, owner ChatOwner, id string) (*v1.Conversation, error) {
	rows, err := list(ctx, d, `SELECT `+conversationColumns+` FROM conversations WHERE id = ? AND provider = ? AND subject = ?`, scanConversation, id, owner.Provider, owner.Subject)
	return one(rows, err, "conversation", id)
}

// Fetches one of the owner's conversations with its turns and settings
func (d *DB) GetConversation(ctx context.Context, owner ChatOwner, id string) (*v1.Conversation, error) {
	rows, err := list(ctx, d, `SELECT `+conversationColumns+`, body FROM conversations WHERE id = ? AND provider = ? AND subject = ?`, func(rows *sql.Rows) (*v1.Conversation, error) {
		c := &v1.Conversation{}
		var body string
		if err := rows.Scan(&c.Id, &c.Title, &c.Model, &c.TurnCount, at{&c.CreatedAt}, at{&c.UpdatedAt}, &body); err != nil {
			return nil, err
		}
		saved := &v1.Conversation{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(body), saved); err != nil {
			return nil, err
		}
		c.Settings, c.Turns = saved.GetSettings(), saved.GetTurns()
		return c, nil
	}, id, owner.Provider, owner.Subject)
	return one(rows, err, "conversation", id)
}

// Retitles one of the owner's conversations, reporting existence
func (d *DB) RenameConversation(ctx context.Context, owner ChatOwner, id, title, updated string) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `UPDATE conversations SET title = ?, updated_at = ? WHERE id = ? AND provider = ? AND subject = ?`, title, updated, id, owner.Provider, owner.Subject)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Removes one of the owner's conversations, reporting existence
func (d *DB) DeleteConversation(ctx context.Context, owner ChatOwner, id string) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM conversations WHERE id = ? AND provider = ? AND subject = ?`, id, owner.Provider, owner.Subject)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Stores an image for the owner's conversations, replacing one with the same id.
// Reports false when the id belongs to another account.
func (d *DB) PutChatFile(ctx context.Context, owner ChatOwner, f *v1.ChatFile, data []byte, created string) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `INSERT INTO chat_files (id, provider, subject, media_type, name, width, height, data, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET media_type = excluded.media_type, name = excluded.name, width = excluded.width, height = excluded.height, data = excluded.data
		WHERE chat_files.provider = excluded.provider AND chat_files.subject = excluded.subject`,
		f.GetId(), owner.Provider, owner.Subject, f.GetMediaType(), f.GetName(), f.GetWidth(), f.GetHeight(), data, created)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Fetches one of the owner's stored images with its bytes
func (d *DB) GetChatFile(ctx context.Context, owner ChatOwner, id string) (*v1.ChatFile, []byte, error) {
	type row struct {
		file *v1.ChatFile
		data []byte
	}
	rows, err := list(ctx, d, `SELECT id, media_type, name, width, height, data FROM chat_files WHERE id = ? AND provider = ? AND subject = ?`, func(rows *sql.Rows) (row, error) {
		f := &v1.ChatFile{}
		var data []byte
		if err := rows.Scan(&f.Id, &f.MediaType, &f.Name, &f.Width, &f.Height, &data); err != nil {
			return row{}, err
		}
		f.SizeBytes = uint64(len(data))
		return row{file: f, data: data}, nil
	}, id, owner.Provider, owner.Subject)
	got, err := one(rows, err, "chat file", id)
	if err != nil {
		return nil, nil, err
	}
	return got.file, got.data, nil
}

// Removes the owner's stored images with the given ids, skipping ids it does not own
func (d *DB) DeleteChatFiles(ctx context.Context, owner ChatOwner, ids []string) error {
	return d.tx(ctx, func(exec execFn) error {
		for _, id := range ids {
			if err := exec(`DELETE FROM chat_files WHERE id = ? AND provider = ? AND subject = ?`, id, owner.Provider, owner.Subject); err != nil {
				return err
			}
		}
		return nil
	})
}
