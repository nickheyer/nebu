package bots

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestBotConnectsRegistersAndAnswersMentions(t *testing.T) {
	h := newHarness(t)
	bot := h.ready(t, quickSpec())
	s := h.session()
	if s == nil || !s.opened {
		t.Fatal("no session opened")
	}
	waitFor(t, "commands registered", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.commands["g1"]) == 7
	})
	got, _ := h.m.Get(bot.GetId())
	if got.GetStatus().GetUsername() != "nebu-bot" || got.GetStatus().GetInviteUrl() == "" || len(got.GetStatus().GetShards()) != 1 || !got.GetStatus().GetShards()[0].GetConnected() {
		t.Fatalf("status %v", got.GetStatus())
	}
	// Ignore messages that do not address the bot.
	s.h.message(0, plain("1", "just chatting"))
	time.Sleep(150 * time.Millisecond)
	if s.sentCount() != 0 {
		t.Fatalf("answered an unaddressed message: %v", s.sent)
	}
	s.history["c1"] = []*discordgo.Message{plain("1", "just chatting")}
	s.h.message(0, mention("2", "hello there"))
	waitFor(t, "an answer", func() bool { return s.sentCount() == 1 })
	if s.sent[0].msg.Content != "Hello from llm." {
		t.Fatalf("answer %q", s.sent[0].msg.Content)
	}
	req := h.upstream.lastRequest()
	if req["model"] != "llm" {
		t.Fatalf("model %v", req["model"])
	}
	msgs := req["messages"].([]any)
	system := msgs[0].(map[string]any)
	if system["role"] != "system" || !strings.Contains(system["content"].(string), "Your name is Nova") || !strings.Contains(system["content"].(string), "Test Guild") {
		t.Fatalf("system prompt %v", system["content"])
	}
	if last := lastUserText(req); last != "nick: just chatting\nnick: hello there" {
		t.Fatalf("history folded wrong: %q", last)
	}
	got, _ = h.m.Get(bot.GetId())
	if got.GetStatus().GetReplies() != 1 {
		t.Fatalf("replies %d", got.GetStatus().GetReplies())
	}
	acts, _ := h.m.Activity(bot.GetId(), 0)
	found := false
	for _, a := range acts {
		if a.GetKind() == "reply" && a.GetTrace() != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no reply activity with a trace: %v", acts)
	}
}

func TestWakeWordsPickThePersonaAndWebhooksWearIt(t *testing.T) {
	h := newHarness(t)
	spec := quickSpec()
	spec.Personas = append(spec.Personas, &v1.Persona{Name: "Sam", Model: "llm", Webhook: true, AvatarUrl: "https://example.com/sam.png"})
	bot := h.ready(t, spec)
	s := h.session()
	s.h.message(0, plain("5", "sam what do you think"))
	waitFor(t, "a webhook post", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.hooks) == 1
	})
	if s.hooks[0].Username != "Sam" || s.hooks[0].AvatarURL != "https://example.com/sam.png" || s.hooks[0].Content != "Hello from llm." {
		t.Fatalf("webhook params %+v", s.hooks[0])
	}
	if s.sentCount() != 0 {
		t.Fatal("a webhook persona must not post as the bot")
	}
	rows, err := h.m.DB.ListBotChannels(context.Background(), bot.GetId())
	if err != nil || len(rows) != 1 || rows[0].WebhookID != "hook-c1" {
		t.Fatalf("webhook not remembered: %v %v", rows, err)
	}
	// Own webhook messages are assistant turns. Other personas are user turns.
	s.history["c1"] = []*discordgo.Message{
		{ID: "5", ChannelID: "c1", Content: "sam what do you think", Author: &discordgo.User{ID: "u1", Username: "nick"}, Timestamp: time.Now()},
		{ID: "6", ChannelID: "c1", Content: "Hello from llm.", Author: &discordgo.User{ID: "hook-c1", Username: "Sam", Bot: true}, WebhookID: "hook-c1", Timestamp: time.Now()},
	}
	s.h.message(0, plain("7", "sam, again?"))
	waitFor(t, "a second webhook post", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.hooks) == 2
	})
	msgs := h.upstream.lastRequest()["messages"].([]any)
	roles := []string{}
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "system,user,assistant,user" {
		t.Fatalf("roles %v", roles)
	}
}

func TestHumanizedPersonaSplitsAndLowercases(t *testing.T) {
	h := newHarness(t)
	spec := quickSpec()
	spec.Personas[0].Humanize = &v1.Humanize{Enabled: true, CharsPerSecond: 100000, SplitMessages: true, Casual: true, QuoteReply: true, ReactionChance: 1, Reactions: []string{"👀"}}
	h.ready(t, spec)
	s := h.session()
	s.h.message(0, mention("10", "give me two paragraphs"))
	waitFor(t, "two messages", func() bool { return s.sentCount() == 2 })
	if s.sent[0].msg.Content != "first para" || s.sent[1].msg.Content != "second para" {
		t.Fatalf("chunks %q %q", s.sent[0].msg.Content, s.sent[1].msg.Content)
	}
	if s.sent[0].msg.Reference == nil || s.sent[0].msg.Reference.MessageID != "10" || s.sent[1].msg.Reference != nil {
		t.Fatal("only the first chunk quotes the message")
	}
	s.mu.Lock()
	reactions := append([]string(nil), s.reactions...)
	typed := s.typing
	s.mu.Unlock()
	if len(reactions) != 1 || reactions[0] != "👀" {
		t.Fatalf("reactions %v", reactions)
	}
	if typed == 0 {
		t.Fatal("the typing indicator never showed")
	}
}

func TestHumanizedPersonaKeepsQuietOnFailure(t *testing.T) {
	h := newHarness(t)
	spec := quickSpec()
	spec.Personas[0].Humanize = &v1.Humanize{Enabled: true}
	bot := h.ready(t, spec)
	s := h.session()
	s.h.message(0, mention("11", "please fail"))
	waitFor(t, "an error counted", func() bool {
		b, _ := h.m.Get(bot.GetId())
		return b.GetStatus().GetErrors() == 1
	})
	time.Sleep(50 * time.Millisecond)
	if s.sentCount() != 0 {
		t.Fatalf("a humanized persona posted an error: %v", s.sent[0].msg.Content)
	}
	// Visible personas report errors.
	spec = quickSpec()
	if _, err := h.m.Update(context.Background(), &v1.UpdateBotRequest{Id: bot.GetId(), Enabled: true, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "restart", func() bool {
		b, _ := h.m.Get(bot.GetId())
		return b.GetState() == v1.BotState_BOT_STATE_READY && h.session() != s
	})
	if !s.closed {
		t.Fatal("the old session was not closed on update")
	}
	s2 := h.session()
	s2.h.message(0, mention("12", "please fail"))
	waitFor(t, "an error message", func() bool { return s2.sentCount() == 1 })
	if !strings.Contains(s2.sent[0].msg.Content, "the model fell over") {
		t.Fatalf("error text %q", s2.sent[0].msg.Content)
	}
}

func TestStreamingPersonaEditsAsItGoes(t *testing.T) {
	h := newHarness(t)
	spec := quickSpec()
	spec.Personas[0].Stream = true
	h.ready(t, spec)
	s := h.session()
	s.h.message(0, mention("20", "stream please"))
	waitFor(t, "the final edit", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.edits) > 0 && s.edits[len(s.edits)-1] == "Hello from llm."
	})
	if s.sentCount() != 1 {
		t.Fatalf("streamed answer posted %d messages", s.sentCount())
	}
}

func TestPrefixCommandsMakeImagesAndVideo(t *testing.T) {
	h := newHarness(t)
	bot := h.ready(t, quickSpec())
	s := h.session()
	s.h.message(0, plain("30", "!imagine a cat in a hat"))
	waitFor(t, "an image post", func() bool { return s.sentCount() == 1 })
	if len(s.sent[0].msg.Files) != 1 || s.sent[0].msg.Files[0].Name != "image-1.png" || !strings.Contains(s.sent[0].msg.Content, "a cat in a hat") {
		t.Fatalf("image post %+v", s.sent[0].msg)
	}
	job := h.upstream.lastJob()
	if job["kind"] != "img_gen" || job["request"].(map[string]any)["prompt"] != "a cat in a hat" {
		t.Fatalf("job %v", job)
	}
	s.h.message(0, plain("31", "!video the cat waves"))
	waitFor(t, "a video post", func() bool { return s.sentCount() == 2 })
	if len(s.sent[1].msg.Files) != 1 || s.sent[1].msg.Files[0].Name != "video.webm" || s.sent[1].msg.Files[0].ContentType != "video/webm" {
		t.Fatalf("video post %+v", s.sent[1].msg)
	}
	b, _ := h.m.Get(bot.GetId())
	if b.GetStatus().GetImages() != 1 || b.GetStatus().GetVideos() != 1 {
		t.Fatalf("counters %v", b.GetStatus())
	}
	s.h.message(0, plain("32", "!help"))
	waitFor(t, "help", func() bool { return s.sentCount() == 3 })
	if !strings.Contains(s.sent[2].msg.Content, "/imagine") || !strings.Contains(s.sent[2].msg.Content, "!imagine") {
		t.Fatalf("help %q", s.sent[2].msg.Content)
	}
	s.h.message(0, plain("33", "!models"))
	waitFor(t, "models", func() bool { return s.sentCount() == 4 })
	if !strings.Contains(s.sent[3].msg.Content, "`llm` chat") || !strings.Contains(s.sent[3].msg.Content, "`sd` images, video") {
		t.Fatalf("models %q", s.sent[3].msg.Content)
	}
}

func TestSlashCommandsAnswerWithFollowups(t *testing.T) {
	h := newHarness(t)
	spec := quickSpec()
	spec.Personas = append(spec.Personas, &v1.Persona{Name: "Sam", Model: "llm"})
	bot := h.ready(t, spec)
	s := h.session()
	ask := &discordgo.Interaction{Type: discordgo.InteractionApplicationCommand, ChannelID: "c1", GuildID: "g1", Member: &discordgo.Member{User: &discordgo.User{ID: "u1", Username: "nick"}},
		Data: discordgo.ApplicationCommandInteractionData{Name: "ask", Options: []*discordgo.ApplicationCommandInteractionDataOption{{Name: "prompt", Type: discordgo.ApplicationCommandOptionString, Value: "what time is it"}}}}
	s.h.interact(0, ask)
	waitFor(t, "a followup", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.followups) == 1
	})
	if s.responses[0].Type != discordgo.InteractionResponseDeferredChannelMessageWithSource || s.followups[0].Content != "Hello from llm." {
		t.Fatalf("interaction flow %+v %+v", s.responses[0], s.followups[0])
	}
	if last := lastUserText(h.upstream.lastRequest()); last != "nick: what time is it" {
		t.Fatalf("prompt %q", last)
	}
	// Persona selection persists. Reset clears prior history.
	choose := &discordgo.Interaction{Type: discordgo.InteractionApplicationCommand, ChannelID: "c1", GuildID: "g1", Member: &discordgo.Member{User: &discordgo.User{ID: "u1", Username: "nick"}},
		Data: discordgo.ApplicationCommandInteractionData{Name: "persona", Options: []*discordgo.ApplicationCommandInteractionDataOption{{Name: "name", Type: discordgo.ApplicationCommandOptionString, Value: "sam"}}}}
	s.h.interact(0, choose)
	waitFor(t, "persona chosen", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.followups) == 2
	})
	if s.followups[1].Content != "Sam speaks here now." || s.followups[1].Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Fatalf("persona answer %+v", s.followups[1])
	}
	rows, _ := h.m.DB.ListBotChannels(context.Background(), bot.GetId())
	if len(rows) != 1 || rows[0].PersonaID != bot.GetSpec().GetPersonas()[1].GetId() {
		t.Fatalf("channel persona not saved: %+v", rows)
	}
	s.history["c1"] = []*discordgo.Message{plain("40", "old news"), plain("41", "older news")}
	reset := &discordgo.Interaction{Type: discordgo.InteractionApplicationCommand, ChannelID: "c1", GuildID: "g1", Member: &discordgo.Member{User: &discordgo.User{ID: "u1", Username: "nick"}},
		Data: discordgo.ApplicationCommandInteractionData{Name: "reset"}}
	s.h.interact(0, reset)
	waitFor(t, "reset", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.followups) == 3
	})
	s.h.message(0, mention("42", "and now?"))
	waitFor(t, "an answer after reset", func() bool { return s.sentCount() == 1 })
	if last := lastUserText(h.upstream.lastRequest()); last != "nick: and now?" {
		t.Fatalf("history was not cut at the reset: %q", last)
	}
	system := h.upstream.lastRequest()["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "Your name is Sam") {
		t.Fatalf("the chosen persona did not answer: %q", system)
	}
}

func TestAutomationsFireOnKeywordsAndSchedules(t *testing.T) {
	h := newHarness(t)
	spec := quickSpec()
	spec.Automations = []*v1.Automation{
		{Name: "pong", Enabled: true, Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_KEYWORD, Pattern: `\bping (\w+)`}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_TEXT, Template: "pong {{.Author}} {{index .Match 1}}", Reply: true}},
		{Name: "greet", Enabled: true, Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_COMMAND, Command: "roll"}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_CHAT, Template: "Roll a die for {{.Author}}: {{.Content}}"}},
		{Name: "react", Enabled: true, Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_MENTION}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_REACT, Emoji: "🔥"}},
		{Name: "ticker", Enabled: true, Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_SCHEDULE, EverySeconds: 1}, ChannelIds: []string{"c1"}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_TEXT, Template: "tick in {{.Channel}}"}},
	}
	bot := h.ready(t, spec)
	s := h.session()
	s.h.message(0, plain("50", "ping pong-table"))
	waitFor(t, "pong", func() bool { return s.sentCount() >= 1 })
	if s.sent[0].msg.Content != "pong nick pong" || s.sent[0].msg.Reference == nil {
		t.Fatalf("keyword automation %+v", s.sent[0].msg)
	}
	s.h.message(0, plain("51", "!roll 2d6"))
	waitFor(t, "roll", func() bool {
		for _, m := range s.sentSnapshot() {
			if m.Content == "Hello from llm." {
				return true
			}
		}
		return false
	})
	if last := lastUserText(h.upstream.lastRequest()); last != "Roll a die for nick: 2d6" {
		t.Fatalf("command template %q", last)
	}
	s.h.message(0, mention("52", "look"))
	waitFor(t, "a reaction and an answer", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.reactions) == 1
	})
	// Use a shorter schedule interval in tests.
	waitFor(t, "a tick", func() bool {
		for _, m := range s.sentSnapshot() {
			if m.Content == "tick in general" {
				return true
			}
		}
		return false
	})
	last, err := h.m.DB.ListBotSchedules(context.Background(), bot.GetId())
	if err != nil || last[bot.GetSpec().GetAutomations()[3].GetId()] == "" {
		t.Fatalf("schedule run not recorded: %v %v", last, err)
	}
}

func (f *fakeSession) sentSnapshot() []*discordgo.MessageSend {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*discordgo.MessageSend
	for _, s := range f.sent {
		out = append(out, s.msg)
	}
	return out
}

func TestEngagementRules(t *testing.T) {
	h := newHarness(t)
	spec := quickSpec()
	spec.Engagement.DirectMessages = false
	spec.Engagement.DeniedChannelIds = []string{"c1"}
	spec.Engagement.AnswerBots = false
	h.ready(t, spec)
	s := h.session()
	s.h.message(0, mention("60", "denied channel"))
	s.h.message(0, &discordgo.Message{ID: "61", ChannelID: "dm", Content: "a dm", Author: &discordgo.User{ID: "u1", Username: "nick"}})
	fromBot := plain("62", "<@1001> beep")
	fromBot.Author.Bot = true
	fromBot.Mentions = []*discordgo.User{{ID: "1001"}}
	fromBot.ChannelID = "c2"
	s.channels["c2"] = &discordgo.Channel{ID: "c2", Name: "other", GuildID: "g1"}
	s.h.message(0, fromBot)
	time.Sleep(200 * time.Millisecond)
	if s.sentCount() != 0 {
		t.Fatalf("rules ignored: %v", s.sentSnapshot())
	}
	ok := mention("63", "allowed channel")
	ok.ChannelID = "c2"
	s.h.message(0, ok)
	waitFor(t, "an answer in the allowed channel", func() bool { return s.sentCount() == 1 })
	if s.sent[0].channel != "c2" {
		t.Fatalf("answered in %s", s.sent[0].channel)
	}
}

func TestBotChainAndCooldown(t *testing.T) {
	h := newHarness(t)
	spec := quickSpec()
	spec.Engagement.AnswerBots = true
	spec.Engagement.MaxBotChain = 2
	spec.Engagement.CooldownMs = 60000
	h.ready(t, spec)
	s := h.session()
	other := &discordgo.User{ID: "b2", Username: "otherbot", Bot: true}
	s.history["c1"] = []*discordgo.Message{{ID: "70", ChannelID: "c1", Content: "beep", Author: other, Timestamp: time.Now()}}
	chained := &discordgo.Message{ID: "71", ChannelID: "c1", GuildID: "g1", Content: "<@1001> boop", Author: other, Mentions: []*discordgo.User{{ID: "1001"}}, Timestamp: time.Now()}
	s.h.message(0, chained)
	time.Sleep(150 * time.Millisecond)
	if s.sentCount() != 0 {
		t.Fatal("answered inside a bot chain past the limit")
	}
	s.h.message(0, mention("72", "first"))
	waitFor(t, "first answer", func() bool { return s.sentCount() == 1 })
	s.h.message(0, mention("73", "second, too soon"))
	time.Sleep(150 * time.Millisecond)
	if s.sentCount() != 1 {
		t.Fatal("cooldown ignored")
	}
}

func TestSendNowAndGuilds(t *testing.T) {
	h := newHarness(t)
	bot := h.ready(t, quickSpec())
	s := h.session()
	guilds, err := h.m.Guilds(context.Background(), bot.GetId())
	if err != nil || len(guilds) != 1 || guilds[0].GetName() != "Test Guild" || len(guilds[0].GetChannels()) != 1 || guilds[0].GetChannels()[0].GetKind() != "text" {
		t.Fatalf("guilds %v %v", guilds, err)
	}
	resp, err := h.m.Send(context.Background(), &v1.SendBotMessageRequest{BotId: "tester", ChannelId: "c1", Kind: v1.ActionKind_ACTION_KIND_TEXT, Content: "announcement"})
	if err != nil || resp.GetMessageId() == "" {
		t.Fatalf("send %v %v", resp, err)
	}
	resp, err = h.m.Send(context.Background(), &v1.SendBotMessageRequest{BotId: bot.GetId(), ChannelId: "c1", Kind: v1.ActionKind_ACTION_KIND_CHAT, Content: "say hi"})
	if err != nil || resp.GetTrace() == "" {
		t.Fatalf("chat send %v %v", resp, err)
	}
	resp, err = h.m.Send(context.Background(), &v1.SendBotMessageRequest{BotId: bot.GetId(), ChannelId: "c1", Kind: v1.ActionKind_ACTION_KIND_IMAGE, Content: "a lighthouse"})
	if err != nil {
		t.Fatal(err)
	}
	snap := s.sentSnapshot()
	if len(snap) != 3 || snap[0].Content != "announcement" || snap[1].Content != "Hello from llm." || len(snap[2].Files) != 1 {
		t.Fatalf("posts %+v", snap)
	}
	if _, err := h.m.Send(context.Background(), &v1.SendBotMessageRequest{BotId: "nope", ChannelId: "c1", Content: "x"}); err == nil {
		t.Fatal("unknown bot accepted")
	}
}

func TestStopStartDeleteAndValidation(t *testing.T) {
	h := newHarness(t)
	bot := h.ready(t, quickSpec())
	s := h.session()
	stopped, err := h.m.Stop(context.Background(), bot.GetName())
	if err != nil || stopped.GetState() != v1.BotState_BOT_STATE_STOPPED || stopped.GetEnabled() || !s.closed {
		t.Fatalf("stop %v %v closed=%v", stopped, err, s.closed)
	}
	if _, err := h.m.Send(context.Background(), &v1.SendBotMessageRequest{BotId: bot.GetId(), ChannelId: "c1", Content: "x"}); err == nil {
		t.Fatal("a stopped bot took a message")
	}
	if _, err := h.m.Start(context.Background(), bot.GetId()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "started again", func() bool {
		b, _ := h.m.Get(bot.GetId())
		return b.GetState() == v1.BotState_BOT_STATE_READY && b.GetEnabled()
	})
	rows, _ := h.m.DB.ListBots(context.Background())
	if len(rows) != 1 || !rows[0].Bot.GetEnabled() || rows[0].Token != "secret" {
		t.Fatalf("rows %+v", rows)
	}
	if _, err := h.m.Create(context.Background(), &v1.CreateBotRequest{Name: "Tester", Token: "x"}); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if _, err := h.m.Create(context.Background(), &v1.CreateBotRequest{Name: "other"}); err == nil {
		t.Fatal("missing token accepted")
	}
	bad := quickSpec()
	bad.Automations = []*v1.Automation{{Name: "x", Enabled: true, Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_KEYWORD, Pattern: "("}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_TEXT, Template: "y"}}}
	if _, err := h.m.Update(context.Background(), &v1.UpdateBotRequest{Id: bot.GetId(), Enabled: true, Spec: bad}); err == nil || !strings.Contains(err.Error(), "pattern") {
		t.Fatalf("bad pattern accepted: %v", err)
	}
	deleted, err := h.m.Delete(context.Background(), bot.GetId())
	if err != nil || deleted.GetId() != bot.GetId() {
		t.Fatal(err)
	}
	if _, err := h.m.Get(bot.GetId()); err == nil {
		t.Fatal("deleted bot still known")
	}
	rows, _ = h.m.DB.ListBots(context.Background())
	if len(rows) != 0 {
		t.Fatal("row not deleted")
	}
}

func TestLoadRecoversEnabledBots(t *testing.T) {
	h := newHarness(t)
	bot := h.ready(t, quickSpec())
	h.m.Close()
	again := New(context.Background(), h.m.DB, h.m.Gateway, h.m.Events, h.m.Log, "")
	again.dial, again.probe = h.m.dial, h.m.probe
	if err := again.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := again.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(again.Close)
	waitFor(t, "recovered bot ready", func() bool {
		b, err := again.Get(bot.GetId())
		return err == nil && b.GetState() == v1.BotState_BOT_STATE_READY
	})
}
