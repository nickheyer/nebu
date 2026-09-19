// Package bots runs Discord bots that route chat, images, and video through the gateway.
package bots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	// Returned when a bot id or name is not known
	ErrUnknownBot = errors.New("unknown bot")
	// Returned when a bot's settings are malformed
	ErrBot = errors.New("invalid bot")
	// Returned when a call needs the bot connected and it is not
	ErrNotRunning = errors.New("bot not running")
)

const (
	// Activity lines kept per bot
	activityMax = 500
	// Minimum interval between counter and latency updates.
	statusFlush = time.Second
	// Invite permissions: view channels, send messages and thread replies, embed links,
	// attach files, read history, react, use external emojis, and manage persona webhooks.
	invitePermissions = 1<<10 | 1<<11 | 1<<38 | 1<<14 | 1<<15 | 1<<16 | 1<<6 | 1<<18 | 1<<29
)

// Manages stored bots, active runners, and UI events.
type Manager struct {
	DB      *db.DB
	Gateway *gateway.Gateway
	Events  *events.Bus
	Log     *slog.Logger
	// ffmpeg path for video sampling. Empty uses PATH.
	FFmpeg string
	// Session factory, replaced in tests.
	dial dialer
	// Token lookup, replaced in tests.
	probe func(ctx context.Context, token string) (*v1.ProbeBotTokenResponse, error)

	mu   sync.Mutex
	bots map[string]*entry
	base context.Context
}

type entry struct {
	bot   *v1.Bot
	token string
	// Validation error for stored settings. Invalid bots stay disconnected.
	invalid  string
	run      *runner
	activity []*v1.BotActivity
	// Pending status update for throttled publishing.
	flush *time.Timer
}

// Builds a manager whose runners live under base
func New(base context.Context, store *db.DB, gw *gateway.Gateway, bus *events.Bus, log *slog.Logger, ffmpeg string) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{DB: store, Gateway: gw, Events: bus, Log: log, FFmpeg: ffmpeg, dial: dialDiscord, probe: probeToken, bots: map[string]*entry{}, base: base}
}

// Loads bots without connecting them.
func (m *Manager) Load(ctx context.Context) error {
	rows, err := m.DB.ListBots(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		e := &entry{bot: r.Bot, token: r.Token}
		r.Bot.State = v1.BotState_BOT_STATE_STOPPED
		r.Bot.Status = &v1.BotStatus{}
		// Mark invalid stored settings as failed.
		if err := normalize(r.Bot.GetName(), r.Bot.Spec); err != nil {
			e.invalid = err.Error()
			r.Bot.State, r.Bot.Error = v1.BotState_BOT_STATE_FAILED, "the stored settings no longer pass validation, save them again: "+err.Error()
			m.Log.Warn("bot settings did not load cleanly", "bot", r.Bot.GetName(), "err", err)
		}
		m.bots[r.Bot.GetId()] = e
	}
	return nil
}

// Connects every enabled bot
func (m *Manager) Recover(ctx context.Context) error {
	m.mu.Lock()
	var start []*entry
	for _, e := range m.bots {
		if e.bot.GetEnabled() && e.invalid == "" {
			start = append(start, e)
		}
	}
	m.mu.Unlock()
	for _, e := range start {
		m.mu.Lock()
		m.startLocked(e)
		m.mu.Unlock()
	}
	return nil
}

// Disconnects every bot
func (m *Manager) Close() {
	m.mu.Lock()
	var stop []*runner
	for _, e := range m.bots {
		if e.run != nil {
			stop = append(stop, e.run)
			e.run = nil
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, r := range stop {
		wg.Add(1)
		go func(r *runner) {
			defer wg.Done()
			r.stop()
		}(r)
	}
	wg.Wait()
}

// Lists every bot by name
func (m *Manager) List() []*v1.Bot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*v1.Bot, 0, len(m.bots))
	for _, e := range m.bots {
		out = append(out, proto.Clone(e.bot).(*v1.Bot))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out
}

// Finds a bot by id or name
func (m *Manager) Get(ref string) (*v1.Bot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, err := m.lookupLocked(ref)
	if err != nil {
		return nil, err
	}
	return proto.Clone(e.bot).(*v1.Bot), nil
}

func (m *Manager) lookupLocked(ref string) (*entry, error) {
	if e, ok := m.bots[ref]; ok {
		return e, nil
	}
	for _, e := range m.bots {
		if strings.EqualFold(e.bot.GetName(), ref) {
			return e, nil
		}
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownBot, ref)
}

func (m *Manager) nameTakenLocked(name, except string) bool {
	for id, e := range m.bots {
		if id != except && strings.EqualFold(e.bot.GetName(), name) {
			return true
		}
	}
	return false
}

// Creates a bot, connecting it when enabled
func (m *Manager) Create(ctx context.Context, req *v1.CreateBotRequest) (*v1.Bot, error) {
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, fmt.Errorf("%w: a name is required", ErrBot)
	}
	token := strings.TrimSpace(req.GetToken())
	if token == "" {
		return nil, fmt.Errorf("%w: a token is required, from Bot under the application in the Discord developer portal", ErrBot)
	}
	spec := proto.Clone(req.GetSpec()).(*v1.BotSpec)
	if spec == nil {
		spec = &v1.BotSpec{}
	}
	if err := normalize(name, spec); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nameTakenLocked(name, "") {
		return nil, fmt.Errorf("%w: a bot named %s exists", ErrBot, name)
	}
	now := timestamppb.Now()
	bot := &v1.Bot{Id: db.NewID(), Name: name, Enabled: req.GetEnabled(), Spec: spec, State: v1.BotState_BOT_STATE_STOPPED, Status: &v1.BotStatus{}, CreatedAt: now, UpdatedAt: now, TokenSet: true}
	if err := m.DB.PutBot(ctx, bot, token); err != nil {
		return nil, err
	}
	e := &entry{bot: bot, token: token}
	m.bots[bot.GetId()] = e
	m.publishLocked(e, v1.EventAction_EVENT_ACTION_CREATED)
	if bot.GetEnabled() {
		m.startLocked(e)
	}
	return proto.Clone(bot).(*v1.Bot), nil
}

// Updates settings and reconnects a running bot.
func (m *Manager) Update(ctx context.Context, req *v1.UpdateBotRequest) (*v1.Bot, error) {
	m.mu.Lock()
	e, err := m.lookupLocked(req.GetId())
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		name = e.bot.GetName()
	}
	if m.nameTakenLocked(name, e.bot.GetId()) {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: a bot named %s exists", ErrBot, name)
	}
	spec := proto.Clone(req.GetSpec()).(*v1.BotSpec)
	if spec == nil {
		spec = &v1.BotSpec{}
	}
	if err := normalize(name, spec); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	token := e.token
	if t := strings.TrimSpace(req.GetToken()); t != "" {
		token = t
	}
	next := proto.Clone(e.bot).(*v1.Bot)
	next.Name, next.Enabled, next.Spec, next.UpdatedAt, next.TokenSet = name, req.GetEnabled(), spec, timestamppb.Now(), token != ""
	if err := m.DB.PutBot(ctx, next, token); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	e.bot, e.token, e.invalid = next, token, ""
	// Reconnect with new settings or start/stop when enabled changes.
	old := e.run
	e.run = nil
	m.publishLocked(e, v1.EventAction_EVENT_ACTION_UPDATED)
	m.mu.Unlock()
	if old != nil {
		old.stop()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.bot.GetEnabled() {
		m.startLocked(e)
	} else {
		m.setStateLocked(e, v1.BotState_BOT_STATE_STOPPED, "")
	}
	return proto.Clone(e.bot).(*v1.Bot), nil
}

// Deletes a bot, disconnecting it first
func (m *Manager) Delete(ctx context.Context, ref string) (*v1.Bot, error) {
	m.mu.Lock()
	e, err := m.lookupLocked(ref)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	old := e.run
	e.run = nil
	m.mu.Unlock()
	if old != nil {
		old.stop()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.DB.DeleteBot(ctx, e.bot.GetId()); err != nil {
		return nil, err
	}
	delete(m.bots, e.bot.GetId())
	e.bot.State = v1.BotState_BOT_STATE_STOPPED
	m.Events.Publish(v1.EventKind_EVENT_KIND_BOT, v1.EventAction_EVENT_ACTION_DELETED, e.bot.GetId(), e.bot)
	return proto.Clone(e.bot).(*v1.Bot), nil
}

// Connects a bot and marks it enabled
func (m *Manager) Start(ctx context.Context, ref string) (*v1.Bot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, err := m.lookupLocked(ref)
	if err != nil {
		return nil, err
	}
	if e.token == "" {
		return nil, fmt.Errorf("%w: %s has no token", ErrBot, e.bot.GetName())
	}
	if e.invalid != "" {
		return nil, fmt.Errorf("%w: %s", ErrBot, e.bot.GetError())
	}
	if !e.bot.GetEnabled() {
		e.bot.Enabled, e.bot.UpdatedAt = true, timestamppb.Now()
		if err := m.DB.PutBot(ctx, e.bot, e.token); err != nil {
			return nil, err
		}
	}
	if e.run == nil {
		m.startLocked(e)
	}
	return proto.Clone(e.bot).(*v1.Bot), nil
}

// Disconnects a bot and marks it disabled
func (m *Manager) Stop(ctx context.Context, ref string) (*v1.Bot, error) {
	m.mu.Lock()
	e, err := m.lookupLocked(ref)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if e.bot.GetEnabled() {
		e.bot.Enabled, e.bot.UpdatedAt = false, timestamppb.Now()
		if err := m.DB.PutBot(ctx, e.bot, e.token); err != nil {
			m.mu.Unlock()
			return nil, err
		}
	}
	old := e.run
	e.run = nil
	if old != nil {
		m.setStateLocked(e, v1.BotState_BOT_STATE_STOPPING, "")
	}
	m.mu.Unlock()
	if old != nil {
		old.stop()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setStateLocked(e, v1.BotState_BOT_STATE_STOPPED, "")
	return proto.Clone(e.bot).(*v1.Bot), nil
}

// Lists recent bot activity, newest first.
func (m *Manager) Activity(ref string, limit int) ([]*v1.BotActivity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, err := m.lookupLocked(ref)
	if err != nil {
		return nil, err
	}
	out := make([]*v1.BotActivity, 0, len(e.activity))
	for i := len(e.activity) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		out = append(out, proto.Clone(e.activity[i]).(*v1.BotActivity))
	}
	return out, nil
}

// Lists the guilds and channels a running bot sees
func (m *Manager) Guilds(ctx context.Context, ref string) ([]*v1.BotGuild, error) {
	r, err := m.running(ref)
	if err != nil {
		return nil, err
	}
	return r.guilds(ctx)
}

// Posts to a channel through a running bot
func (m *Manager) Send(ctx context.Context, req *v1.SendBotMessageRequest) (*v1.SendBotMessageResponse, error) {
	r, err := m.running(req.GetBotId())
	if err != nil {
		return nil, err
	}
	return r.sendNow(ctx, req)
}

// Looks up a token or a named bot's stored token with Discord.
func (m *Manager) Probe(ctx context.Context, req *v1.ProbeBotTokenRequest) (*v1.ProbeBotTokenResponse, error) {
	token := strings.TrimSpace(req.GetToken())
	if token == "" && req.GetBotId() != "" {
		m.mu.Lock()
		e, err := m.lookupLocked(req.GetBotId())
		if err == nil {
			token = e.token
		}
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	if token == "" {
		return nil, fmt.Errorf("%w: a token is required", ErrBot)
	}
	return m.probe(ctx, token)
}

func (m *Manager) running(ref string) (*runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, err := m.lookupLocked(ref)
	if err != nil {
		return nil, err
	}
	if e.run == nil || !e.run.connected() {
		return nil, fmt.Errorf("%w: %s", ErrNotRunning, e.bot.GetName())
	}
	return e.run, nil
}

func (m *Manager) startLocked(e *entry) {
	e.bot.Status = &v1.BotStatus{}
	r := newRunner(m, e.bot, e.token)
	e.run = r
	m.setStateLocked(e, v1.BotState_BOT_STATE_CONNECTING, "")
	go r.run()
}

// Stores and publishes bot state, including any error.
func (m *Manager) setStateLocked(e *entry, state v1.BotState, errText string) {
	e.bot.State, e.bot.Error = state, errText
	m.publishLocked(e, v1.EventAction_EVENT_ACTION_UPDATED)
}

func (m *Manager) publishLocked(e *entry, action v1.EventAction) {
	if e.flush != nil {
		e.flush.Stop()
		e.flush = nil
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_BOT, action, e.bot.GetId(), e.bot)
}

// Applies state updates only from the bot's current runner.
func (m *Manager) setState(r *runner, state v1.BotState, errText string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.bots[r.id]
	if !ok || e.run != r {
		return
	}
	m.setStateLocked(e, state, errText)
}

// Applies runner status updates, throttled by the flush interval.
func (m *Manager) status(r *runner, fn func(*v1.BotStatus)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.bots[r.id]
	if !ok || e.run != r {
		return
	}
	if e.bot.Status == nil {
		e.bot.Status = &v1.BotStatus{}
	}
	fn(e.bot.Status)
	if e.flush == nil {
		e.flush = time.AfterFunc(statusFlush, func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if e.flush == nil {
				return
			}
			e.flush = nil
			m.Events.Publish(v1.EventKind_EVENT_KIND_BOT, v1.EventAction_EVENT_ACTION_UPDATED, e.bot.GetId(), e.bot)
		})
	}
}

// Records and publishes an activity entry.
func (m *Manager) record(r *runner, a *v1.BotActivity) {
	a.BotId, a.At = r.id, timestamppb.Now()
	m.mu.Lock()
	e, ok := m.bots[r.id]
	if ok {
		e.activity = append(e.activity, a)
		if len(e.activity) > activityMax {
			e.activity = e.activity[len(e.activity)-activityMax:]
		}
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	attrs := []any{"bot", r.name, "kind", a.GetKind(), "channel", a.GetChannelId()}
	switch a.GetLevel() {
	case "error":
		m.Log.Warn("bot: "+a.GetMessage(), attrs...)
	case "warn":
		m.Log.Info("bot: "+a.GetMessage(), attrs...)
	default:
		m.Log.Debug("bot: "+a.GetMessage(), attrs...)
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_BOT_ACTIVITY, v1.EventAction_EVENT_ACTION_CREATED, r.id, a)
}

// Looks up the bot identity and recommended shard count.
func probeToken(ctx context.Context, token string) (*v1.ProbeBotTokenResponse, error) {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	s.UserAgent = "nebu (https://github.com/nickheyer/nebu)"
	user, err := s.User("@me", discordgo.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("discord refused the token: %s", describeREST(err))
	}
	raw, err := s.Request("GET", discordgo.EndpointAPI+"oauth2/applications/@me", nil, discordgo.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("discord did not name the application: %s", describeREST(err))
	}
	var app struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &app); err != nil {
		return nil, err
	}
	gw, err := s.GatewayBot(discordgo.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("discord did not answer the gateway query: %s", describeREST(err))
	}
	return &v1.ProbeBotTokenResponse{
		UserId:            user.ID,
		Username:          user.Username,
		ApplicationId:     app.ID,
		AvatarUrl:         user.AvatarURL("256"),
		RecommendedShards: uint32(gw.Shards),
		InviteUrl:         inviteURL(app.ID),
	}, nil
}

// Bot invite URL with persona permissions.
func inviteURL(appID string) string {
	if appID == "" {
		return ""
	}
	return fmt.Sprintf("https://discord.com/oauth2/authorize?client_id=%s&scope=bot%%20applications.commands&permissions=%d", appID, invitePermissions)
}
