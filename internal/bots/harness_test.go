package bots

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A Discord that records what the bot does and lets a test hand it events
type fakeSession struct {
	mu        sync.Mutex
	h         handlers
	shard     int
	self      *discordgo.User
	opened    bool
	closed    bool
	sent      []sent
	edits     []string
	hooks     []*discordgo.WebhookParams
	hookEdits []string
	reactions []string
	followups []*discordgo.WebhookParams
	responses []*discordgo.InteractionResponse
	commands  map[string][]*discordgo.ApplicationCommand
	presence  []discordgo.UpdateStatusData
	typing    int
	history   map[string][]*discordgo.Message
	channels  map[string]*discordgo.Channel
	webhooks  map[string]*discordgo.Webhook
	failSend  bool
}

type sent struct {
	channel string
	msg     *discordgo.MessageSend
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		self:     &discordgo.User{ID: "1001", Username: "nebu-bot"},
		commands: map[string][]*discordgo.ApplicationCommand{},
		history:  map[string][]*discordgo.Message{},
		channels: map[string]*discordgo.Channel{"c1": {ID: "c1", Name: "general", GuildID: "g1", Type: discordgo.ChannelTypeGuildText}, "dm": {ID: "dm", Type: discordgo.ChannelTypeDM}},
		webhooks: map[string]*discordgo.Webhook{},
	}
}

func (f *fakeSession) Open() error {
	f.mu.Lock()
	f.opened = true
	f.mu.Unlock()
	f.h.ready(f.shard, &discordgo.Ready{User: f.self, Application: &discordgo.Application{ID: "app1"}})
	return nil
}

func (f *fakeSession) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) Self() *discordgo.User  { return f.self }
func (f *fakeSession) ApplicationID() string  { return "app1" }
func (f *fakeSession) Latency() time.Duration { return 42 * time.Millisecond }
func (f *fakeSession) Typing(string) error    { f.mu.Lock(); f.typing++; f.mu.Unlock(); return nil }
func (f *fakeSession) Guilds() []*discordgo.Guild {
	return []*discordgo.Guild{{ID: "g1", Name: "Test Guild", MemberCount: 3, SystemChannelID: "c1", Channels: []*discordgo.Channel{f.channels["c1"]}}}
}

func (f *fakeSession) SendMessage(channelID string, data *discordgo.MessageSend) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSend {
		return nil, fmt.Errorf("HTTP 403 Forbidden")
	}
	f.sent = append(f.sent, sent{channel: channelID, msg: data})
	return &discordgo.Message{ID: fmt.Sprintf("m%d", len(f.sent)), ChannelID: channelID, Content: data.Content}, nil
}

func (f *fakeSession) EditMessage(channelID, messageID, content string) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, content)
	return &discordgo.Message{ID: messageID, Content: content}, nil
}

func (f *fakeSession) Messages(channelID string, limit int, beforeID string) ([]*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*discordgo.Message
	for i := len(f.history[channelID]) - 1; i >= 0 && len(out) < limit; i-- {
		m := f.history[channelID][i]
		if beforeID != "" && !newer(beforeID, m.ID) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func (f *fakeSession) React(channelID, messageID, emoji string) error {
	f.mu.Lock()
	f.reactions = append(f.reactions, emoji)
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) Channel(channelID string) (*discordgo.Channel, error) {
	if c, ok := f.channels[channelID]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("no channel %s", channelID)
}

func (f *fakeSession) Guild(guildID string) (*discordgo.Guild, error) {
	return f.Guilds()[0], nil
}

func (f *fakeSession) GuildChannels(guildID string) ([]*discordgo.Channel, error) {
	return []*discordgo.Channel{f.channels["c1"]}, nil
}

func (f *fakeSession) Webhooks(channelID string) ([]*discordgo.Webhook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*discordgo.Webhook
	for _, h := range f.webhooks {
		if h.ChannelID == channelID {
			out = append(out, h)
		}
	}
	return out, nil
}

func (f *fakeSession) CreateWebhook(channelID, name string) (*discordgo.Webhook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := &discordgo.Webhook{ID: "hook-" + channelID, Token: "tok", Name: name, ChannelID: channelID}
	f.webhooks[h.ID] = h
	return h, nil
}

func (f *fakeSession) ExecuteWebhook(id, token, threadID string, data *discordgo.WebhookParams) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hooks = append(f.hooks, data)
	return &discordgo.Message{ID: fmt.Sprintf("w%d", len(f.hooks)), WebhookID: id, Content: data.Content}, nil
}

func (f *fakeSession) EditWebhookMessage(id, token, messageID string, data *discordgo.WebhookEdit) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if data.Content != nil {
		f.hookEdits = append(f.hookEdits, *data.Content)
	}
	return &discordgo.Message{ID: messageID}, nil
}

func (f *fakeSession) SetPresence(data discordgo.UpdateStatusData) error {
	f.mu.Lock()
	f.presence = append(f.presence, data)
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) Respond(i *discordgo.Interaction, r *discordgo.InteractionResponse) error {
	f.mu.Lock()
	f.responses = append(f.responses, r)
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) Followup(i *discordgo.Interaction, data *discordgo.WebhookParams) (*discordgo.Message, error) {
	f.mu.Lock()
	f.followups = append(f.followups, data)
	f.mu.Unlock()
	return &discordgo.Message{ID: "f1"}, nil
}

func (f *fakeSession) EditFollowup(i *discordgo.Interaction, messageID string, data *discordgo.WebhookEdit) (*discordgo.Message, error) {
	return &discordgo.Message{ID: messageID}, nil
}

func (f *fakeSession) OverwriteCommands(appID, guildID string, cmds []*discordgo.ApplicationCommand) error {
	f.mu.Lock()
	f.commands[guildID] = cmds
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) GatewayBot() (*discordgo.GatewayBotResponse, error) {
	return &discordgo.GatewayBotResponse{Shards: 1}, nil
}

// The number of messages sent so far
func (f *fakeSession) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// A runtime that answers chat the OpenAI way and images and video the stable-diffusion.cpp way
type fakeUpstream struct {
	mu       sync.Mutex
	requests []map[string]any
	jobs     map[string]map[string]any
	polls    map[string]int
}

func newFakeUpstream() *fakeUpstream {
	return &fakeUpstream{jobs: map[string]map[string]any{}, polls: map[string]int{}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// The last user message's text in an OpenAI request
func lastUserText(req map[string]any) string {
	msgs, _ := req["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m, _ := msgs[i].(map[string]any)
		if m["role"] != "user" {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			return c
		case []any:
			for _, p := range c {
				part, _ := p.(map[string]any)
				if part["type"] == "text" {
					return part["text"].(string)
				}
			}
		}
	}
	return ""
}

func (f *fakeUpstream) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &req)
		f.mu.Lock()
		f.requests = append(f.requests, req)
		f.mu.Unlock()
		text := "Hello from llm."
		last := lastUserText(req)
		switch {
		case strings.Contains(last, "two paragraphs"):
			text = "First Para.\n\nSecond Para."
		case strings.Contains(last, "fail"):
			writeJSON(w, 500, map[string]any{"error": map[string]any{"message": "the model fell over"}})
			return
		}
		if stream, _ := req["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			for _, piece := range []string{"Hello", " from", " llm."} {
				chunk, _ := json.Marshal(map[string]any{"id": "x", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": piece}}}})
				fmt.Fprintf(w, "data: %s\n\n", chunk)
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				time.Sleep(20 * time.Millisecond)
			}
			done, _ := json.Marshal(map[string]any{"id": "x", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", done)
			return
		}
		writeJSON(w, 200, map[string]any{"id": "x", "object": "chat.completion", "model": req["model"], "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2}})
	})
	submit := func(kind string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			json.Unmarshal(raw, &body)
			f.mu.Lock()
			id := fmt.Sprintf("job_%s_%d", kind, len(f.jobs))
			f.jobs[id] = map[string]any{"id": id, "kind": kind, "status": "queued", "request": body}
			f.mu.Unlock()
			writeJSON(w, 202, map[string]any{"id": id, "kind": kind, "status": "queued"})
		}
	}
	mux.HandleFunc("/sdcpp/v1/img_gen", submit("img_gen"))
	mux.HandleFunc("/sdcpp/v1/vid_gen", submit("vid_gen"))
	mux.HandleFunc("/sdcpp/v1/jobs/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/sdcpp/v1/jobs/")
		f.mu.Lock()
		defer f.mu.Unlock()
		job, ok := f.jobs[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		f.polls[id]++
		if f.polls[id] >= 2 {
			job["status"] = "completed"
			req := job["request"].(map[string]any)
			if job["kind"] == "img_gen" {
				n, _ := req["batch_count"].(float64)
				var images []map[string]any
				for i := 0; i < max(int(n), 1); i++ {
					images = append(images, map[string]any{"index": i, "b64_json": base64.StdEncoding.EncodeToString([]byte("png-bytes"))})
				}
				job["result"] = map[string]any{"output_format": "png", "images": images}
			} else {
				job["result"] = map[string]any{"output_format": "webm", "mime_type": "video/webm", "fps": 16, "frame_count": 33, "b64_json": base64.StdEncoding.EncodeToString([]byte("webm-bytes"))}
			}
		}
		writeJSON(w, 200, job)
	})
	return mux
}

func (f *fakeUpstream) lastRequest() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return nil
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeUpstream) lastJob() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var newest map[string]any
	for _, j := range f.jobs {
		newest = j
	}
	return newest
}

// A manager over a temporary store and a gateway with one language model and one diffusion model
type harness struct {
	m        *Manager
	upstream *fakeUpstream
	mu       sync.Mutex
	sessions []*fakeSession
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	bus := events.New()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	up := newFakeUpstream()
	srv := httptest.NewServer(up.handler())
	t.Cleanup(srv.Close)
	table, err := gateway.OpenTable(ctx, nil, bus, log)
	if err != nil {
		t.Fatal(err)
	}
	table.Set("llm", "inst-llm", "", srv.URL, "repo:llm", "", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, nil)
	table.SetModes("inst-sd", []string{"img_gen", "vid_gen"})
	table.Set("sd", "inst-sd", "", srv.URL, "repo:sd", "", v1.ApiFlavor_API_FLAVOR_SDCPP, nil, nil)
	gw := gateway.New(table, nil, nil, nil, bus, log)
	// Clocks tuned for a test's patience
	scheduleTick, videoPoll, streamEdit = 200*time.Millisecond, 50*time.Millisecond, 10*time.Millisecond
	h := &harness{upstream: up}
	h.m = New(ctx, store, gw, bus, log, "")
	h.m.dial = func(token string, shard, count int, intents discordgo.Intent, hs handlers) (session, error) {
		s := newFakeSession()
		s.h, s.shard = hs, shard
		h.mu.Lock()
		h.sessions = append(h.sessions, s)
		h.mu.Unlock()
		return s, nil
	}
	h.m.probe = func(ctx context.Context, token string) (*v1.ProbeBotTokenResponse, error) {
		return &v1.ProbeBotTokenResponse{UserId: "1001", Username: "nebu-bot", ApplicationId: "app1", RecommendedShards: 1, InviteUrl: inviteURL("app1")}, nil
	}
	if err := h.m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.m.Close)
	return h
}

// The newest session opened
func (h *harness) session() *fakeSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.sessions) == 0 {
		return nil
	}
	return h.sessions[len(h.sessions)-1]
}

// Creates a bot and waits until it is ready
func (h *harness) ready(t *testing.T, spec *v1.BotSpec) *v1.Bot {
	t.Helper()
	bot, err := h.m.Create(context.Background(), &v1.CreateBotRequest{Name: "tester", Token: "secret", Enabled: true, Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bot ready", func() bool {
		b, _ := h.m.Get(bot.GetId())
		return b.GetState() == v1.BotState_BOT_STATE_READY
	})
	return bot
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A message from a person mentioning the bot
func mention(id, content string) *discordgo.Message {
	return &discordgo.Message{ID: id, ChannelID: "c1", GuildID: "g1", Content: "<@1001> " + content, Author: &discordgo.User{ID: "u1", Username: "nick"}, Mentions: []*discordgo.User{{ID: "1001"}}, Timestamp: time.Now()}
}

// A plain message in the channel
func plain(id, content string) *discordgo.Message {
	return &discordgo.Message{ID: id, ChannelID: "c1", GuildID: "g1", Content: content, Author: &discordgo.User{ID: "u1", Username: "nick"}, Timestamp: time.Now()}
}

// A spec with one persona on the language model, quick to answer
func quickSpec() *v1.BotSpec {
	return &v1.BotSpec{
		ShardCount: 1,
		Personas:   []*v1.Persona{{Name: "Nova", Model: "llm", ImageModel: "sd", VideoModel: "sd", Vision: true, WakeWords: []string{"hey nova"}}},
		Engagement: &v1.Engagement{DirectMessages: true, RequireMention: true, Prefix: "!"},
		Commands:   &v1.Commands{Enabled: true, GuildIds: []string{"g1"}},
	}
}
