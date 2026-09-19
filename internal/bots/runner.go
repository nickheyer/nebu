package bots

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Discord requires five seconds between identifies per bucket.
	identifyGap = 5 * time.Second
	// Shard reconnect delay.
	reopenBackoff = 15 * time.Second
	// Renew typing indicators before their ten-second expiry.
	typingRenew = 8 * time.Second
	// Generation timeout.
	generationTimeout = 30 * time.Minute
	// Default persona webhook name.
	webhookPrefix = "nebu "
	// Shard status polling interval.
	statusRefresh = 30 * time.Second
)

// Connected bot state, shards, and active work.
type runner struct {
	m     *Manager
	id    string
	name  string
	spec  *v1.BotSpec
	token string
	// Reaches the gateway in process
	client *http.Client
	// Fetches attachments
	web *http.Client

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	shards []*shard
	self   *discordgo.User
	appID  string
	// Commands are registered once, by the first ready shard.
	registered bool
	channels   map[string]*channelState
	// Last reply time per user, for the user cooldown
	users map[string]time.Time
	// Last run and last cron minute per automation ID.
	lastRun   map[string]time.Time
	lastCron  map[string]time.Time
	sem       chan struct{}
	automata  []*automaton
	personaBy map[string]*v1.Persona
}

// One shard's session and state
type shard struct {
	id        int
	sess      session
	connected bool
	since     time.Time
	err       string
}

// Per-channel runner state.
type channelState struct {
	row       db.BotChannel
	lastReply time.Time
	// Active generation. Each channel allows one reply at a time.
	busy bool
}

// A compiled automation
type automaton struct {
	a       *v1.Automation
	cron    *cronLine
	pattern *regexp.Regexp
	tmpl    *template.Template
}

func newRunner(m *Manager, bot *v1.Bot, token string) *runner {
	ctx, cancel := context.WithCancel(m.base)
	r := &runner{
		m:        m,
		id:       bot.GetId(),
		name:     bot.GetName(),
		spec:     proto.Clone(bot.GetSpec()).(*v1.BotSpec),
		token:    token,
		web:      &http.Client{Timeout: 2 * time.Minute},
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
		channels: map[string]*channelState{},
		users:    map[string]time.Time{},
		lastRun:  map[string]time.Time{},
		lastCron: map[string]time.Time{},
	}
	r.client = m.Gateway.Client("bot:" + bot.GetName())
	r.sem = make(chan struct{}, int(r.spec.GetEngagement().GetMaxConcurrent()))
	r.personaBy = map[string]*v1.Persona{}
	for _, p := range r.spec.GetPersonas() {
		r.personaBy[p.GetId()] = p
	}
	for _, a := range r.spec.GetAutomations() {
		if !a.GetEnabled() {
			continue
		}
		c := &automaton{a: a}
		if t := a.GetTrigger(); t.GetCron() != "" {
			c.cron, _ = parseCron(t.GetCron())
		}
		if t := a.GetTrigger(); t.GetKind() == v1.TriggerKind_TRIGGER_KIND_KEYWORD {
			c.pattern, _ = regexp.Compile("(?i)" + t.GetPattern())
		}
		if tpl := a.GetAction().GetTemplate(); tpl != "" {
			c.tmpl, _ = template.New(a.GetName()).Parse(tpl)
		}
		r.automata = append(r.automata, c)
	}
	return r
}

// Connects this host's shards until stopped.
func (r *runner) run() {
	defer close(r.done)
	rows, err := r.m.DB.ListBotChannels(r.ctx, r.id)
	if err != nil {
		r.fail("channel state could not be read: " + err.Error())
		return
	}
	for _, row := range rows {
		r.channels[row.ChannelID] = &channelState{row: row}
	}
	if last, err := r.m.DB.ListBotSchedules(r.ctx, r.id); err == nil {
		for id, at := range last {
			if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
				r.lastRun[id] = t
			}
		}
	}
	count := int(r.spec.GetShardCount())
	if count == 0 {
		probe, err := r.m.probe(r.ctx, r.token)
		if err != nil {
			r.fail(err.Error())
			return
		}
		count = max(1, int(probe.GetRecommendedShards()))
		r.m.status(r, func(s *v1.BotStatus) {
			s.RecommendedShards, s.ApplicationId, s.InviteUrl = probe.GetRecommendedShards(), probe.GetApplicationId(), probe.GetInviteUrl()
		})
	}
	ids := make([]int, 0, count)
	if len(r.spec.GetShardIds()) > 0 {
		for _, id := range r.spec.GetShardIds() {
			ids = append(ids, int(id))
		}
	} else {
		for i := 0; i < count; i++ {
			ids = append(ids, i)
		}
	}
	sort.Ints(ids)
	intents := discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent | discordgo.IntentsGuildMessageReactions | discordgo.IntentsDirectMessageReactions
	if r.spec.GetEngagement().GetMemberEvents() {
		intents |= discordgo.IntentsGuildMembers
	}
	h := handlers{ready: r.onReady, resumed: r.onResumed, disconnect: r.onDisconnect, message: r.onMessage, interact: r.onInteraction, memberAdd: r.onMemberAdd, reaction: r.onReaction, guilds: r.onGuilds}
	r.mu.Lock()
	for _, id := range ids {
		r.shards = append(r.shards, &shard{id: id})
	}
	r.mu.Unlock()
	r.m.status(r, func(s *v1.BotStatus) {
		s.StartedAt = timestamppb.Now()
		s.Shards = nil
		for _, id := range ids {
			s.Shards = append(s.Shards, &v1.ShardStatus{Id: uint32(id)})
		}
	})
	for i, sh := range r.snapshotShards() {
		if i > 0 {
			select {
			case <-r.ctx.Done():
				r.closeAll()
				return
			case <-time.After(identifyGap):
			}
		}
		sess, err := r.m.dial(r.token, sh.id, count, intents, h)
		if err != nil {
			r.fail("shard " + strconv.Itoa(sh.id) + ": " + err.Error())
			r.closeAll()
			return
		}
		r.mu.Lock()
		sh.sess = sess
		r.mu.Unlock()
		if err := r.open(sh); err != nil {
			// Token and intent errors stop the bot. Retry other errors with backoff.
			if strings.Contains(err.Error(), "discord refused") {
				r.fail(err.Error())
				r.closeAll()
				return
			}
			r.shardError(sh, err.Error())
			go r.reopen(sh)
		}
	}
	go r.presence()
	go r.schedule()
	go r.heartbeat()
	<-r.ctx.Done()
	r.closeAll()
}

// Opens one shard, reporting its state
func (r *runner) open(sh *shard) error {
	if err := sh.sess.Open(); err != nil {
		return err
	}
	return nil
}

// Retries shard connections until connected or stopped.
func (r *runner) reopen(sh *shard) {
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(reopenBackoff):
		}
		err := r.open(sh)
		if err == nil || errors.Is(err, discordgo.ErrWSAlreadyOpen) {
			return
		}
		if strings.Contains(err.Error(), "discord refused") {
			r.fail(err.Error())
			r.cancel()
			return
		}
		r.shardError(sh, err.Error())
	}
}

func (r *runner) snapshotShards() []*shard {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*shard(nil), r.shards...)
}

func (r *runner) closeAll() {
	for _, sh := range r.snapshotShards() {
		if sh.sess != nil {
			sh.sess.Close()
		}
	}
}

// Ends the runner and waits for its shards to close
func (r *runner) stop() {
	r.cancel()
	<-r.done
}

// Whether at least one shard is connected
func (r *runner) connected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, sh := range r.shards {
		if sh.connected {
			return true
		}
	}
	return false
}

// Returns a connected session for work outside an event handler.
func (r *runner) anySession() (session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, sh := range r.shards {
		if sh.connected && sh.sess != nil {
			return sh.sess, nil
		}
	}
	return nil, fmt.Errorf("%w: %s has no shard connected", ErrNotRunning, r.name)
}

func (r *runner) sessionOf(shardID int) session {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, sh := range r.shards {
		if sh.id == shardID {
			return sh.sess
		}
	}
	return nil
}

func (r *runner) fail(message string) {
	r.m.setState(r, v1.BotState_BOT_STATE_FAILED, message)
	r.activity("error", "shard", message, "", "", "", "")
}

func (r *runner) shardError(sh *shard, message string) {
	r.mu.Lock()
	sh.err, sh.connected = message, false
	r.mu.Unlock()
	r.activity("error", "shard", fmt.Sprintf("shard %d: %s", sh.id, message), "", "", "", "")
	r.refreshState()
}

// Derives and publishes bot state from shard status.
func (r *runner) refreshState() {
	r.mu.Lock()
	up, total := 0, len(r.shards)
	shards := make([]*v1.ShardStatus, 0, total)
	guilds := 0
	for _, sh := range r.shards {
		st := &v1.ShardStatus{Id: uint32(sh.id), Connected: sh.connected, Error: sh.err}
		if sh.connected {
			up++
			st.ConnectedAt = timestamppb.New(sh.since)
			if sh.sess != nil {
				st.Guilds = uint32(len(sh.sess.Guilds()))
				st.LatencyMs = uint32(sh.sess.Latency().Milliseconds())
				guilds += int(st.Guilds)
			}
		}
		shards = append(shards, st)
	}
	self, appID := r.self, r.appID
	r.mu.Unlock()
	r.m.status(r, func(s *v1.BotStatus) {
		s.Shards, s.Guilds = shards, uint32(guilds)
		if self != nil {
			s.UserId, s.Username, s.AvatarUrl = self.ID, self.Username, self.AvatarURL("256")
		}
		if appID != "" {
			s.ApplicationId, s.InviteUrl = appID, inviteURL(appID)
		}
	})
	switch {
	case up == 0:
		r.m.setState(r, v1.BotState_BOT_STATE_CONNECTING, "")
	case up < total:
		r.m.setState(r, v1.BotState_BOT_STATE_DEGRADED, "")
	default:
		r.m.setState(r, v1.BotState_BOT_STATE_READY, "")
	}
}

func (r *runner) onReady(shardID int, ready *discordgo.Ready) {
	r.mu.Lock()
	for _, sh := range r.shards {
		if sh.id == shardID {
			sh.connected, sh.since, sh.err = true, time.Now(), ""
		}
	}
	if ready.User != nil {
		r.self = ready.User
	}
	if ready.Application != nil && ready.Application.ID != "" {
		r.appID = ready.Application.ID
	}
	register := !r.registered && r.spec.GetCommands().GetEnabled()
	if register {
		r.registered = true
	}
	r.mu.Unlock()
	r.activity("info", "shard", fmt.Sprintf("shard %d connected as %s", shardID, ready.User.Username), "", "", "", "")
	r.refreshState()
	if register {
		go r.registerCommands(r.sessionOf(shardID))
	}
	// Compare actual shard count with Discord's recommendation.
	if sess := r.sessionOf(shardID); sess != nil && shardID == r.firstShard() {
		go func() {
			if gw, err := sess.GatewayBot(); err == nil {
				r.m.status(r, func(s *v1.BotStatus) { s.RecommendedShards = uint32(gw.Shards) })
			}
		}()
	}
}

// The lowest shard id this host opens
func (r *runner) firstShard() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.shards) == 0 {
		return 0
	}
	return r.shards[0].id
}

// Periodically refreshes shard latencies and guild counts.
func (r *runner) heartbeat() {
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(statusRefresh):
		}
		if r.connected() {
			r.refreshState()
		}
	}
}

func (r *runner) onResumed(shardID int) {
	r.mu.Lock()
	for _, sh := range r.shards {
		if sh.id == shardID {
			sh.connected, sh.err = true, ""
		}
	}
	r.mu.Unlock()
	r.refreshState()
}

func (r *runner) onDisconnect(shardID int) {
	r.mu.Lock()
	for _, sh := range r.shards {
		if sh.id == shardID {
			sh.connected = false
		}
	}
	r.mu.Unlock()
	if r.ctx.Err() == nil {
		r.activity("warn", "shard", fmt.Sprintf("shard %d disconnected, reconnecting", shardID), "", "", "", "")
		r.refreshState()
	}
}

func (r *runner) onGuilds(int) { r.refreshState() }

// Sets the status and cycles the activity lines
func (r *runner) presence() {
	p := r.spec.GetPresence()
	lines := p.GetActivities()
	i := 0
	for {
		var acts []*discordgo.Activity
		if len(lines) > 0 {
			acts = []*discordgo.Activity{activityOf(p.GetActivityType(), lines[i%len(lines)])}
			i++
		}
		if sess, err := r.anySession(); err == nil {
			if err := sess.SetPresence(discordgo.UpdateStatusData{Status: p.GetStatus(), Activities: acts}); err != nil {
				r.activity("warn", "presence", "presence not set: "+err.Error(), "", "", "", "")
			}
		}
		wait := time.Duration(p.GetRotateSeconds()) * time.Second
		if len(lines) <= 1 {
			// Static activity is set once and restored on reconnect.
			wait = time.Minute
		}
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Updates activity for a presence automation.
func (r *runner) setActivity(text string) error {
	sess, err := r.anySession()
	if err != nil {
		return err
	}
	p := r.spec.GetPresence()
	return sess.SetPresence(discordgo.UpdateStatusData{Status: p.GetStatus(), Activities: []*discordgo.Activity{activityOf(p.GetActivityType(), text)}})
}

func activityOf(kind, text string) *discordgo.Activity {
	a := &discordgo.Activity{Name: text}
	switch kind {
	case "listening":
		a.Type = discordgo.ActivityTypeListening
	case "watching":
		a.Type = discordgo.ActivityTypeWatching
	case "competing":
		a.Type = discordgo.ActivityTypeCompeting
	case "custom":
		a.Type, a.Name, a.State = discordgo.ActivityTypeCustom, "Custom Status", text
	default:
		a.Type = discordgo.ActivityTypeGame
	}
	return a
}

// Records an activity entry.
func (r *runner) activity(level, kind, message, guildID, channelID, persona, trace string) {
	r.m.record(r, &v1.BotActivity{Level: level, Kind: kind, Message: message, GuildId: guildID, ChannelId: channelID, Persona: persona, Trace: trace})
}

func (r *runner) count(fn func(*v1.BotStatus)) { r.m.status(r, fn) }

// Finds a persona by ID, defaulting to the first.
func (r *runner) persona(id string) *v1.Persona {
	if p, ok := r.personaBy[id]; ok && id != "" {
		return p
	}
	return r.spec.GetPersonas()[0]
}

// Finds a persona by name, ignoring case.
func (r *runner) personaNamed(name string) (*v1.Persona, bool) {
	for _, p := range r.spec.GetPersonas() {
		if strings.EqualFold(p.GetName(), strings.TrimSpace(name)) {
			return p, true
		}
	}
	return nil, false
}

// Returns channel state, creating it if needed.
func (r *runner) channel(channelID string) *channelState {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.channels[channelID]
	if !ok {
		c = &channelState{row: db.BotChannel{BotID: r.id, ChannelID: channelID}}
		r.channels[channelID] = c
	}
	return c
}

// Writes a channel's row
func (r *runner) saveChannel(c *channelState) error {
	r.mu.Lock()
	row := c.row
	r.mu.Unlock()
	return r.m.DB.PutBotChannel(context.Background(), row)
}

// Lists the guilds every shard sees with their text channels
func (r *runner) guilds(ctx context.Context) ([]*v1.BotGuild, error) {
	var out []*v1.BotGuild
	for _, sh := range r.snapshotShards() {
		if !sh.connected || sh.sess == nil {
			continue
		}
		for _, g := range sh.sess.Guilds() {
			bg := &v1.BotGuild{Id: g.ID, Name: g.Name, Members: uint32(g.MemberCount)}
			channels := g.Channels
			if len(channels) == 0 {
				list, err := sh.sess.GuildChannels(g.ID)
				if err != nil {
					return nil, fmt.Errorf("guild %s: %s", g.Name, describeREST(err))
				}
				channels = list
			}
			sort.Slice(channels, func(i, j int) bool { return channels[i].Position < channels[j].Position })
			for _, c := range channels {
				kind := channelKind(c.Type)
				if kind == "" {
					continue
				}
				bg.Channels = append(bg.Channels, &v1.BotChannel{Id: c.ID, Name: c.Name, Kind: kind})
			}
			out = append(out, bg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out, nil
}

func channelKind(t discordgo.ChannelType) string {
	switch t {
	case discordgo.ChannelTypeGuildText:
		return "text"
	case discordgo.ChannelTypeGuildNews:
		return "news"
	case discordgo.ChannelTypeGuildVoice, discordgo.ChannelTypeGuildStageVoice:
		return "voice"
	case discordgo.ChannelTypeGuildForum:
		return "forum"
	case discordgo.ChannelTypeGuildPublicThread, discordgo.ChannelTypeGuildPrivateThread, discordgo.ChannelTypeGuildNewsThread:
		return "thread"
	}
	return ""
}

// Posts persona text or generates a reply in the channel.
func (r *runner) sendNow(ctx context.Context, req *v1.SendBotMessageRequest) (*v1.SendBotMessageResponse, error) {
	sess, err := r.anySession()
	if err != nil {
		return nil, err
	}
	p := r.persona(req.GetPersonaId())
	channelID := strings.TrimSpace(req.GetChannelId())
	if channelID == "" {
		return nil, fmt.Errorf("%w: a channel id is required", ErrBot)
	}
	content := strings.TrimSpace(req.GetContent())
	if content == "" {
		return nil, fmt.Errorf("%w: content is required", ErrBot)
	}
	kind := req.GetKind()
	if kind == v1.ActionKind_ACTION_KIND_UNSPECIFIED {
		kind = v1.ActionKind_ACTION_KIND_TEXT
	}
	ctx, cancel := context.WithTimeout(ctx, generationTimeout)
	defer cancel()
	out := &v1.SendBotMessageResponse{}
	post := func(text string, files []file) error {
		msg, err := r.speak(sess, p, channelID, text, files, nil)
		if err != nil {
			return err
		}
		if msg != nil {
			out.MessageId = msg.ID
		}
		return nil
	}
	switch kind {
	case v1.ActionKind_ACTION_KIND_TEXT:
		if err := post(content, nil); err != nil {
			return nil, err
		}
		r.activity("info", "send", "posted text", "", channelID, p.GetName(), "")
	case v1.ActionKind_ACTION_KIND_CHAT:
		model, err := r.route(p, "chat")
		if err != nil {
			return nil, err
		}
		chat := r.freshChat(p, sess, channelID, content)
		ans, err := r.chat(ctx, p, model, chat, nil)
		if ans != nil {
			out.Trace = ans.trace
		}
		if err != nil {
			return nil, err
		}
		if err := post(r.style(p, ans.text), nil); err != nil {
			return nil, err
		}
		r.count(func(s *v1.BotStatus) { s.Replies++ })
		r.activity("info", "send", "posted an answer", "", channelID, p.GetName(), ans.trace)
	case v1.ActionKind_ACTION_KIND_IMAGE:
		model, err := r.route(p, "image")
		if err != nil {
			return nil, err
		}
		files, trace, err := r.images(ctx, p, model, content, "", 0)
		out.Trace = trace
		if err != nil {
			return nil, err
		}
		if err := post("", files); err != nil {
			return nil, err
		}
		r.count(func(s *v1.BotStatus) { s.Images += uint64(len(files)) })
		r.activity("info", "send", fmt.Sprintf("posted %d images", len(files)), "", channelID, p.GetName(), trace)
	case v1.ActionKind_ACTION_KIND_VIDEO:
		model, err := r.route(p, "video")
		if err != nil {
			return nil, err
		}
		f, trace, err := r.video(ctx, p, model, content, "", nil)
		out.Trace = trace
		if err != nil {
			return nil, err
		}
		if err := r.fits(f); err != nil {
			return nil, err
		}
		if err := post("", []file{*f}); err != nil {
			return nil, err
		}
		r.count(func(s *v1.BotStatus) { s.Videos++ })
		r.activity("info", "send", "posted a video", "", channelID, p.GetName(), trace)
	default:
		return nil, fmt.Errorf("%w: kind %s cannot be sent by hand", ErrBot, kind)
	}
	return out, nil
}

// Checks the Discord upload limit.
func (r *runner) fits(f *file) error {
	if limit := r.spec.GetMedia().GetMaxUploadBytes(); uint64(len(f.data)) > limit {
		return fmt.Errorf("%s is %d bytes, exceeding the %d byte upload limit. Request a smaller video or raise media.max_upload_bytes if your server allows it", f.name, len(f.data), limit)
	}
	return nil
}

// Snowflake IDs increase with time.
func newer(a, b string) bool {
	x, _ := strconv.ParseUint(a, 10, 64)
	y, _ := strconv.ParseUint(b, 10, 64)
	return x > y
}
