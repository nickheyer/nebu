package db

import (
	"context"
	"database/sql"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// Stored bot with its token separate from the public record.
type BotRow struct {
	Bot   *v1.Bot
	Token string
}

// Inserts or replaces a bot with its token and settings
func (d *DB) PutBot(ctx context.Context, b *v1.Bot, token string) error {
	spec, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(b.GetSpec())
	if err != nil {
		return err
	}
	// Upsert by ID so duplicate names fail without replacing another bot.
	_, err = d.sql.ExecContext(ctx, `INSERT INTO bots (id, name, token, enabled, spec, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, token = excluded.token, enabled = excluded.enabled, spec = excluded.spec, created_at = excluded.created_at, updated_at = excluded.updated_at`,
		b.GetId(), b.GetName(), token, boolCol(b.GetEnabled()), string(spec), stamp(b.GetCreatedAt().AsTime()), stamp(b.GetUpdatedAt().AsTime()))
	return err
}

// Lists every bot by name, each with its token
func (d *DB) ListBots(ctx context.Context) ([]BotRow, error) {
	return list(ctx, d, `SELECT id, name, token, enabled, spec, created_at, updated_at FROM bots ORDER BY name`, func(rows *sql.Rows) (BotRow, error) {
		b := &v1.Bot{Spec: &v1.BotSpec{}}
		var token, spec string
		if err := rows.Scan(&b.Id, &b.Name, &token, (*flag)(&b.Enabled), &spec, at{&b.CreatedAt}, at{&b.UpdatedAt}); err != nil {
			return BotRow{}, err
		}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(spec), b.Spec); err != nil {
			return BotRow{}, err
		}
		b.TokenSet = token != ""
		return BotRow{Bot: b, Token: token}, nil
	})
}

// Removes a bot with its channel rows, reporting existence
func (d *DB) DeleteBot(ctx context.Context, id string) (bool, error) {
	return d.del(ctx, "bots", "id", id)
}

// Stored bot channel state.
type BotChannel struct {
	BotID        string
	ChannelID    string
	PersonaID    string
	Cutoff       string
	WebhookID    string
	WebhookToken string
}

// Inserts or replaces a bot's channel row
func (d *DB) PutBotChannel(ctx context.Context, c BotChannel) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO bot_channels (bot_id, channel_id, persona_id, cutoff_message_id, webhook_id, webhook_token) VALUES (?, ?, ?, ?, ?, ?)`,
		c.BotID, c.ChannelID, c.PersonaID, c.Cutoff, c.WebhookID, c.WebhookToken)
	return err
}

// Lists a bot's channel rows
func (d *DB) ListBotChannels(ctx context.Context, botID string) ([]BotChannel, error) {
	return list(ctx, d, `SELECT bot_id, channel_id, persona_id, cutoff_message_id, webhook_id, webhook_token FROM bot_channels WHERE bot_id = ? ORDER BY channel_id`, func(rows *sql.Rows) (BotChannel, error) {
		var c BotChannel
		return c, rows.Scan(&c.BotID, &c.ChannelID, &c.PersonaID, &c.Cutoff, &c.WebhookID, &c.WebhookToken)
	}, botID)
}

// Records when a scheduled automation last ran
func (d *DB) PutBotSchedule(ctx context.Context, botID, automationID string, last string) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO bot_schedules (bot_id, automation_id, last_run_at) VALUES (?, ?, ?)`, botID, automationID, last)
	return err
}

// Returns last run times keyed by automation ID.
func (d *DB) ListBotSchedules(ctx context.Context, botID string) (map[string]string, error) {
	return d.stringMap(ctx, `SELECT automation_id, last_run_at FROM bot_schedules WHERE bot_id = ?`, botID)
}
