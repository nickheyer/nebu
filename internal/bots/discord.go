package bots

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
)

// What one shard asks of Discord, discordgo behind it and a fake in tests
type session interface {
	Open() error
	Close() error
	Self() *discordgo.User
	ApplicationID() string
	Guilds() []*discordgo.Guild
	Latency() time.Duration
	SendMessage(channelID string, data *discordgo.MessageSend) (*discordgo.Message, error)
	EditMessage(channelID, messageID, content string) (*discordgo.Message, error)
	Typing(channelID string) error
	Messages(channelID string, limit int, beforeID string) ([]*discordgo.Message, error)
	React(channelID, messageID, emoji string) error
	Channel(channelID string) (*discordgo.Channel, error)
	Guild(guildID string) (*discordgo.Guild, error)
	GuildChannels(guildID string) ([]*discordgo.Channel, error)
	Webhooks(channelID string) ([]*discordgo.Webhook, error)
	CreateWebhook(channelID, name string) (*discordgo.Webhook, error)
	ExecuteWebhook(id, token, threadID string, data *discordgo.WebhookParams) (*discordgo.Message, error)
	EditWebhookMessage(id, token, messageID string, data *discordgo.WebhookEdit) (*discordgo.Message, error)
	SetPresence(data discordgo.UpdateStatusData) error
	Respond(i *discordgo.Interaction, r *discordgo.InteractionResponse) error
	Followup(i *discordgo.Interaction, data *discordgo.WebhookParams) (*discordgo.Message, error)
	EditFollowup(i *discordgo.Interaction, messageID string, data *discordgo.WebhookEdit) (*discordgo.Message, error)
	OverwriteCommands(appID, guildID string, cmds []*discordgo.ApplicationCommand) error
	GatewayBot() (*discordgo.GatewayBotResponse, error)
}

// What a shard reports back to its runner
type handlers struct {
	ready      func(shard int, r *discordgo.Ready)
	resumed    func(shard int)
	disconnect func(shard int)
	message    func(shard int, m *discordgo.Message)
	interact   func(shard int, i *discordgo.Interaction)
	memberAdd  func(shard int, m *discordgo.Member)
	reaction   func(shard int, r *discordgo.MessageReactionAdd)
	guilds     func(shard int)
}

// Opens sessions, one per shard
type dialer func(token string, shard, count int, intents discordgo.Intent, h handlers) (session, error)

// The discordgo session behind the interface
type dgSession struct {
	s *discordgo.Session
}

func dialDiscord(token string, shard, count int, intents discordgo.Intent, h handlers) (session, error) {
	s, err := discordgo.New("Bot " + strings.TrimSpace(token))
	if err != nil {
		return nil, err
	}
	s.ShardID, s.ShardCount = shard, count
	s.Identify.Intents = intents
	s.Identify.Shard = &[2]int{shard, count}
	s.StateEnabled = true
	s.State.MaxMessageCount = 0
	s.State.TrackVoice = false
	s.State.TrackPresences = false
	s.UserAgent = "nebu (https://github.com/nickheyer/nebu)"
	s.AddHandler(func(_ *discordgo.Session, r *discordgo.Ready) { h.ready(shard, r) })
	s.AddHandler(func(_ *discordgo.Session, _ *discordgo.Resumed) { h.resumed(shard) })
	s.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) { h.disconnect(shard) })
	s.AddHandler(func(_ *discordgo.Session, m *discordgo.MessageCreate) { h.message(shard, m.Message) })
	s.AddHandler(func(_ *discordgo.Session, i *discordgo.InteractionCreate) { h.interact(shard, i.Interaction) })
	s.AddHandler(func(_ *discordgo.Session, m *discordgo.GuildMemberAdd) { h.memberAdd(shard, m.Member) })
	s.AddHandler(func(_ *discordgo.Session, r *discordgo.MessageReactionAdd) { h.reaction(shard, r) })
	s.AddHandler(func(_ *discordgo.Session, _ *discordgo.GuildCreate) { h.guilds(shard) })
	s.AddHandler(func(_ *discordgo.Session, _ *discordgo.GuildDelete) { h.guilds(shard) })
	return &dgSession{s: s}, nil
}

func (d *dgSession) Open() error  { return describeGateway(d.s.Open()) }
func (d *dgSession) Close() error { return d.s.Close() }

func (d *dgSession) Self() *discordgo.User {
	if d.s.State != nil && d.s.State.User != nil {
		return d.s.State.User
	}
	return nil
}

func (d *dgSession) ApplicationID() string {
	if d.s.State != nil && d.s.State.Application != nil {
		return d.s.State.Application.ID
	}
	return ""
}

func (d *dgSession) Guilds() []*discordgo.Guild {
	if d.s.State == nil {
		return nil
	}
	d.s.State.RLock()
	defer d.s.State.RUnlock()
	return append([]*discordgo.Guild(nil), d.s.State.Guilds...)
}

func (d *dgSession) Latency() time.Duration { return d.s.HeartbeatLatency() }

func (d *dgSession) SendMessage(channelID string, data *discordgo.MessageSend) (*discordgo.Message, error) {
	return d.s.ChannelMessageSendComplex(channelID, data)
}

func (d *dgSession) EditMessage(channelID, messageID, content string) (*discordgo.Message, error) {
	return d.s.ChannelMessageEdit(channelID, messageID, content)
}

func (d *dgSession) Typing(channelID string) error { return d.s.ChannelTyping(channelID) }

func (d *dgSession) Messages(channelID string, limit int, beforeID string) ([]*discordgo.Message, error) {
	return d.s.ChannelMessages(channelID, limit, beforeID, "", "")
}

func (d *dgSession) React(channelID, messageID, emoji string) error {
	return d.s.MessageReactionAdd(channelID, messageID, emojiRef(emoji))
}

// Channels come from the shard's state when it holds them, Discord otherwise
func (d *dgSession) Channel(channelID string) (*discordgo.Channel, error) {
	if c, err := d.s.State.Channel(channelID); err == nil {
		return c, nil
	}
	return d.s.Channel(channelID)
}

func (d *dgSession) Guild(guildID string) (*discordgo.Guild, error) {
	if g, err := d.s.State.Guild(guildID); err == nil {
		return g, nil
	}
	return d.s.Guild(guildID)
}

func (d *dgSession) GuildChannels(guildID string) ([]*discordgo.Channel, error) {
	return d.s.GuildChannels(guildID)
}

func (d *dgSession) Webhooks(channelID string) ([]*discordgo.Webhook, error) {
	return d.s.ChannelWebhooks(channelID)
}

func (d *dgSession) CreateWebhook(channelID, name string) (*discordgo.Webhook, error) {
	return d.s.WebhookCreate(channelID, name, "")
}

func (d *dgSession) ExecuteWebhook(id, token, threadID string, data *discordgo.WebhookParams) (*discordgo.Message, error) {
	if threadID != "" {
		return d.s.WebhookThreadExecute(id, token, true, threadID, data)
	}
	return d.s.WebhookExecute(id, token, true, data)
}

func (d *dgSession) EditWebhookMessage(id, token, messageID string, data *discordgo.WebhookEdit) (*discordgo.Message, error) {
	return d.s.WebhookMessageEdit(id, token, messageID, data)
}

func (d *dgSession) SetPresence(data discordgo.UpdateStatusData) error {
	return d.s.UpdateStatusComplex(data)
}

func (d *dgSession) Respond(i *discordgo.Interaction, r *discordgo.InteractionResponse) error {
	return d.s.InteractionRespond(i, r)
}

func (d *dgSession) Followup(i *discordgo.Interaction, data *discordgo.WebhookParams) (*discordgo.Message, error) {
	return d.s.FollowupMessageCreate(i, true, data)
}

func (d *dgSession) EditFollowup(i *discordgo.Interaction, messageID string, data *discordgo.WebhookEdit) (*discordgo.Message, error) {
	return d.s.FollowupMessageEdit(i, messageID, data)
}

func (d *dgSession) OverwriteCommands(appID, guildID string, cmds []*discordgo.ApplicationCommand) error {
	_, err := d.s.ApplicationCommandBulkOverwrite(appID, guildID, cmds)
	return err
}

func (d *dgSession) GatewayBot() (*discordgo.GatewayBotResponse, error) { return d.s.GatewayBot() }

// Puts a gateway refusal into words: the close codes Discord uses for a bad token and for intents the portal has not granted
func describeGateway(err error) error {
	if err == nil {
		return nil
	}
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		switch ce.Code {
		case 4004:
			return fmt.Errorf("discord refused the token: %s", ce.Text)
		case 4013:
			return fmt.Errorf("discord refused the intents as invalid: %s", ce.Text)
		case 4014:
			return fmt.Errorf("discord refused a privileged intent; enable Message Content and, for member events, Server Members under Bot in the developer portal: %s", ce.Text)
		}
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "4004"):
		return fmt.Errorf("discord refused the token: %w", err)
	case strings.Contains(s, "4014"):
		return fmt.Errorf("discord refused a privileged intent; enable Message Content and, for member events, Server Members under Bot in the developer portal: %w", err)
	}
	return err
}

// The message Discord put in a REST error, the error's own text otherwise
func describeREST(err error) string {
	var re *discordgo.RESTError
	if errors.As(err, &re) && re.Message != nil && re.Message.Message != "" {
		return re.Message.Message
	}
	return err.Error()
}

// Whether a REST error says the bot lacks a permission in the channel
func missingPermission(err error) bool {
	var re *discordgo.RESTError
	return errors.As(err, &re) && re.Message != nil && (re.Message.Code == discordgo.ErrCodeMissingPermissions || re.Message.Code == discordgo.ErrCodeMissingAccess)
}

// Whether a REST error says a webhook is gone
func unknownWebhook(err error) bool {
	var re *discordgo.RESTError
	return errors.As(err, &re) && re.Message != nil && re.Message.Code == discordgo.ErrCodeUnknownWebhook
}

// The form a reaction endpoint takes: a unicode emoji as it is, a custom one as name:id
func emojiRef(emoji string) string {
	e := strings.TrimSpace(emoji)
	if strings.HasPrefix(e, "<") && strings.HasSuffix(e, ">") {
		e = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(e, "<"), "a"), ">")
		e = strings.TrimPrefix(e, ":")
	}
	return e
}

// The name a unicode or custom emoji goes by in a reaction event
func emojiName(e *discordgo.Emoji) string {
	if e == nil {
		return ""
	}
	if e.ID != "" {
		return e.Name + ":" + e.ID
	}
	return e.Name
}

// Whether a reaction's emoji is the one a trigger names, by unicode, by name, or by name:id
func sameEmoji(want string, e *discordgo.Emoji) bool {
	if e == nil {
		return false
	}
	want = emojiRef(want)
	return want == e.Name || want == e.Name+":"+e.ID || (e.ID != "" && want == e.ID)
}
