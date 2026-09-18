package bots

import (
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestNormalizeFillsDefaults(t *testing.T) {
	s := &v1.BotSpec{}
	if err := normalize("bot", s); err != nil {
		t.Fatal(err)
	}
	if len(s.Personas) != 1 || s.Personas[0].Name != "bot" || s.Personas[0].Id == "" || !s.Personas[0].Vision {
		t.Fatalf("default persona %v", s.Personas)
	}
	if !s.Engagement.RequireMention || !s.Engagement.DirectMessages || s.Engagement.MaxBotChain != 3 || s.Engagement.MaxConcurrent != 4 {
		t.Fatalf("engagement %v", s.Engagement)
	}
	if s.Commands.Names["imagine"] != "imagine" || !s.Commands.Enabled {
		t.Fatalf("commands %v", s.Commands)
	}
	if s.Memory.Messages != 20 || s.Memory.MaxChars != 8000 || !s.Memory.IncludeNames {
		t.Fatalf("memory %v", s.Memory)
	}
	if s.Media.ImageSize != "1024x1024" || s.Media.MaxUploadBytes != 10_000_000 || s.Media.ImageCount != 1 {
		t.Fatalf("media %v", s.Media)
	}
	if s.Presence.Status != "online" || s.Presence.ActivityType != "playing" || s.Presence.RotateSeconds != 300 {
		t.Fatalf("presence %v", s.Presence)
	}
	if s.Personas[0].Humanize.CharsPerSecond != 45 || s.Personas[0].Humanize.MaxTypingMs != 12000 || s.Personas[0].Humanize.MaxChunkChars != 2000 {
		t.Fatalf("humanize %v", s.Personas[0].Humanize)
	}
}

func TestNormalizeRefusesWhatCannotRun(t *testing.T) {
	cases := []struct {
		name string
		spec *v1.BotSpec
		want string
	}{
		{"shard ids without count", &v1.BotSpec{ShardIds: []uint32{0}}, "shard ids need a shard count"},
		{"shard id past count", &v1.BotSpec{ShardCount: 2, ShardIds: []uint32{2}}, "past the shard count"},
		{"bad status", &v1.BotSpec{Presence: &v1.Presence{Status: "away"}}, "presence status"},
		{"bad command name", &v1.BotSpec{Commands: &v1.Commands{Names: map[string]string{"ask": "Ask Me"}}}, "command name"},
		{"unknown role", &v1.BotSpec{Commands: &v1.Commands{Names: map[string]string{"dance": "x"}}}, "command role"},
		{"duplicate command names", &v1.BotSpec{Commands: &v1.Commands{Names: map[string]string{"ask": "go", "help": "go"}}}, "share the name"},
		{"duplicate personas", &v1.BotSpec{Personas: []*v1.Persona{{Name: "A"}, {Name: "a"}}}, "two personas are named"},
		{"nameless persona", &v1.BotSpec{Personas: []*v1.Persona{{Name: "  "}}}, "needs a name"},
		{"bad temperature", &v1.BotSpec{Personas: []*v1.Persona{{Name: "a", Sampling: &v1.Sampling{Temperature: ptr(3.0)}}}}, "temperature"},
		{"delays reversed", &v1.BotSpec{Personas: []*v1.Persona{{Name: "a", Humanize: &v1.Humanize{DelayMinMs: 5, DelayMaxMs: 1}}}}, "shorter than"},
		{"reaction without emojis", &v1.BotSpec{Personas: []*v1.Persona{{Name: "a", Humanize: &v1.Humanize{ReactionChance: 0.5}}}}, "emojis to react"},
		{"bad hours", &v1.BotSpec{Personas: []*v1.Persona{{Name: "a", Humanize: &v1.Humanize{ActiveHours: "9-17"}}}}, "HH:MM-HH:MM"},
		{"bad zone", &v1.BotSpec{Personas: []*v1.Persona{{Name: "a", Humanize: &v1.Humanize{Timezone: "Mars/Olympus"}}}}, "IANA"},
		{"bad image size", &v1.BotSpec{Media: &v1.Media{ImageSize: "1000x1000"}}, "multiples of 16"},
		{"too many images", &v1.BotSpec{Media: &v1.Media{ImageCount: 9}}, "at most 4"},
		{"too much memory", &v1.BotSpec{Memory: &v1.Memory{Messages: 500}}, "at most 100"},
		{"schedule without channels", &v1.BotSpec{Automations: []*v1.Automation{{Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_SCHEDULE, Cron: "* * * * *"}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_TEXT, Template: "x"}}}}, "channels it posts to"},
		{"schedule without clock", &v1.BotSpec{Automations: []*v1.Automation{{ChannelIds: []string{"c"}, Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_SCHEDULE}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_TEXT, Template: "x"}}}}, "cron line or an interval"},
		{"bad cron", &v1.BotSpec{Automations: []*v1.Automation{{ChannelIds: []string{"c"}, Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_SCHEDULE, Cron: "x"}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_TEXT, Template: "x"}}}}, "five fields"},
		{"bad template", &v1.BotSpec{Automations: []*v1.Automation{{Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_MESSAGE}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_TEXT, Template: "{{.Oops"}}}}, "template"},
		{"command without prefix", &v1.BotSpec{Automations: []*v1.Automation{{Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_COMMAND, Command: "roll"}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_TEXT, Template: "x"}}}}, "command prefix"},
		{"member join without intent", &v1.BotSpec{Automations: []*v1.Automation{{Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_MEMBER_JOIN}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_TEXT, Template: "x"}}}}, "member events"},
		{"react on schedule", &v1.BotSpec{Automations: []*v1.Automation{{ChannelIds: []string{"c"}, Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_SCHEDULE, EverySeconds: 5}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_REACT, Emoji: "x"}}}}, "message to react to"},
		{"unknown persona", &v1.BotSpec{Automations: []*v1.Automation{{PersonaId: "ghost", Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_MESSAGE}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_TEXT, Template: "x"}}}}, "persona that does not exist"},
		{"no action", &v1.BotSpec{Automations: []*v1.Automation{{Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_MESSAGE}}}}, "action is required"},
	}
	for _, c := range cases {
		err := normalize("bot", c.spec)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}

func TestNormalizeAcceptsAFullSpec(t *testing.T) {
	s := &v1.BotSpec{
		ShardCount: 4, ShardIds: []uint32{0, 1},
		Presence:   &v1.Presence{Status: "idle", ActivityType: "custom", Activities: []string{"thinking", " "}},
		Engagement: &v1.Engagement{Prefix: "!", MemberEvents: true},
		Commands:   &v1.Commands{Enabled: true, Names: map[string]string{"imagine": "draw"}, Disabled: []string{"video"}},
		Personas: []*v1.Persona{
			{Name: "Nova", Humanize: &v1.Humanize{Enabled: true, ActiveHours: "22:00-06:00", Timezone: "America/New_York", ReactionChance: 0.2, Reactions: []string{"👀"}}},
			{Name: "Sam", Webhook: true},
		},
		Automations: []*v1.Automation{
			{Name: "morning", Enabled: true, ChannelIds: []string{"c1"}, Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_SCHEDULE, Cron: "0 9 * * mon-fri"}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_CHAT, Template: "Say good morning to the channel"}},
			{Name: "welcome", Enabled: true, Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_MEMBER_JOIN}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_TEXT, Template: "welcome {{.Author}}"}},
			{Name: "roll", Enabled: true, Trigger: &v1.Trigger{Kind: v1.TriggerKind_TRIGGER_KIND_COMMAND, Command: "Roll"}, Action: &v1.Action{Kind: v1.ActionKind_ACTION_KIND_TEXT, Template: "{{.Content}}"}},
		},
	}
	if err := normalize("bot", s); err != nil {
		t.Fatal(err)
	}
	if len(s.Presence.Activities) != 1 || s.Commands.Names["imagine"] != "draw" || s.Commands.Names["ask"] != "ask" || s.Automations[2].Trigger.Command != "roll" {
		t.Fatalf("normalized %v", s)
	}
	if s.Personas[0].Id == "" || s.Personas[1].Id == "" || s.Personas[0].Id == s.Personas[1].Id || s.Automations[0].Id == "" {
		t.Fatal("ids not assigned")
	}
}
