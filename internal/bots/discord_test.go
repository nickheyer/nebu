package bots

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func cacheState(t *testing.T, ids ...string) *discordgo.State {
	t.Helper()
	st := discordgo.NewState()
	st.MaxMessageCount = 5
	if err := st.GuildAdd(&discordgo.Guild{ID: "g", Channels: []*discordgo.Channel{{ID: "c", GuildID: "g"}}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := st.MessageAdd(&discordgo.Message{ID: id, ChannelID: "c", Content: "m" + id}); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// Cached history answers newest first when every message asked for is held, Discord answers otherwise
func TestCachedHistory(t *testing.T) {
	st := cacheState(t, "1", "2", "3", "4")
	got, ok := cachedHistory(st, "c", 2, "4")
	if !ok || len(got) != 2 || got[0].ID != "3" || got[1].ID != "2" {
		t.Fatalf("two before 4: %v %v", ok, got)
	}
	got, ok = cachedHistory(st, "c", 1, "")
	if !ok || len(got) != 1 || got[0].ID != "4" {
		t.Fatalf("newest: %v %v", ok, got)
	}
	got[0].Content = "edited"
	if m, _ := st.Message("c", "4"); m.Content != "m4" {
		t.Fatal("a cached answer is a copy")
	}
	if _, ok := cachedHistory(st, "c", 3, "4"); !ok {
		t.Fatal("three before 4 are held")
	}
	if _, ok := cachedHistory(st, "c", 4, "4"); ok {
		t.Fatal("four before 4 are not held")
	}
	if _, ok := cachedHistory(st, "c", 1, "9"); ok {
		t.Fatal("an unknown message is not held")
	}
	if _, ok := cachedHistory(st, "x", 1, ""); ok {
		t.Fatal("an unknown channel is not held")
	}
	if _, ok := cachedHistory(st, "c", 0, ""); ok {
		t.Fatal("no limit means Discord's default")
	}
	if _, ok := cachedHistory(st, "c", 6, ""); ok {
		t.Fatal("more than the cache holds")
	}
	// The cache keeps the newest and follows deletions
	st = cacheState(t, "1", "2", "3", "4", "5", "6")
	if _, ok := cachedHistory(st, "c", 5, "6"); ok {
		t.Fatal("the oldest left the cache")
	}
	if err := st.MessageRemove(&discordgo.Message{ID: "5", ChannelID: "c"}); err != nil {
		t.Fatal(err)
	}
	got, ok = cachedHistory(st, "c", 2, "6")
	if !ok || got[0].ID != "4" || got[1].ID != "3" {
		t.Fatalf("after a deletion: %v %v", ok, got)
	}
	forgetMessages(st)
	if _, ok := cachedHistory(st, "c", 1, ""); ok {
		t.Fatal("forgotten")
	}
}
