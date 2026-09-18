package db

import (
	"context"
	"path/filepath"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestBotsRoundTrip(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	temp := 0.7
	bot := &v1.Bot{
		Id: "b1", Name: "nova", Enabled: true, CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now(),
		Spec: &v1.BotSpec{
			ShardCount:  2,
			Personas:    []*v1.Persona{{Id: "p1", Name: "Nova", Model: "llm", Sampling: &v1.Sampling{Temperature: &temp}, Humanize: &v1.Humanize{Enabled: true, Reactions: []string{"👀"}}}},
			Automations: []*v1.Automation{{Id: "a1", Name: "pong", Enabled: true, Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_KEYWORD, Pattern: "ping"}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_TEXT, Template: "pong"}}},
			Engagement:  &v1.Engagement{Prefix: "!", ChannelIds: []string{"c1"}},
		},
	}
	if err := d.PutBot(ctx, bot, "tok-secret"); err != nil {
		t.Fatal(err)
	}
	rows, err := d.ListBots(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list %v %v", rows, err)
	}
	if rows[0].Token != "tok-secret" || !rows[0].Bot.GetTokenSet() {
		t.Fatal("token lost")
	}
	want := proto.Clone(bot).(*v1.Bot)
	want.TokenSet = true
	if !proto.Equal(rows[0].Bot, want) {
		t.Fatalf("bot differs:\n%v\n%v", rows[0].Bot, want)
	}
	// A second write with the same name and another id is refused, names are unique
	if err := d.PutBot(ctx, &v1.Bot{Id: "b2", Name: "nova", Spec: &v1.BotSpec{}, CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now()}, "x"); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if err := d.PutBotChannel(ctx, BotChannel{BotID: "b1", ChannelID: "c1", PersonaID: "p1", Cutoff: "100", WebhookID: "h", WebhookToken: "ht"}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutBotChannel(ctx, BotChannel{BotID: "b1", ChannelID: "c1", PersonaID: "p1", Cutoff: "200", WebhookID: "h", WebhookToken: "ht"}); err != nil {
		t.Fatal(err)
	}
	channels, err := d.ListBotChannels(ctx, "b1")
	if err != nil || len(channels) != 1 || channels[0].Cutoff != "200" || channels[0].WebhookToken != "ht" {
		t.Fatalf("channels %v %v", channels, err)
	}
	if err := d.PutBotSchedule(ctx, "b1", "a1", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	last, err := d.ListBotSchedules(ctx, "b1")
	if err != nil || last["a1"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("schedules %v %v", last, err)
	}
	ok, err := d.DeleteBot(ctx, "b1")
	if err != nil || !ok {
		t.Fatal(err)
	}
	channels, _ = d.ListBotChannels(ctx, "b1")
	last, _ = d.ListBotSchedules(ctx, "b1")
	if len(channels) != 0 || len(last) != 0 {
		t.Fatal("child rows survived the bot")
	}
}
