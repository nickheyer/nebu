package bots

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Schedule polling interval. Must be less than a minute.
var scheduleTick = 20 * time.Second

// Automation template data.
type triggerData struct {
	// Message text, joining member name, or reaction emoji.
	Content   string
	Author    string
	AuthorID  string
	Channel   string
	ChannelID string
	Guild     string
	GuildID   string
	Now       string
	Persona   string
	Bot       string
	Emoji     string
	// Submatches of a keyword pattern, the whole match first
	Match []string
}

// Runs scheduled automations when due.
func (r *runner) schedule() {
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(scheduleTick):
		}
		if !r.connected() {
			continue
		}
		now := time.Now()
		for _, a := range r.automata {
			t := a.a.GetTrigger()
			if t.GetKind() != v1.TriggerKind_TRIGGER_KIND_SCHEDULE {
				continue
			}
			due := false
			r.mu.Lock()
			if a.cron != nil {
				local := now
				if p := r.persona(a.a.GetPersonaId()); p.GetHumanize().GetTimezone() != "" {
					if loc, err := time.LoadLocation(p.GetHumanize().GetTimezone()); err == nil {
						local = now.In(loc)
					}
				}
				minute := local.Truncate(time.Minute)
				if a.cron.matches(local) && !r.lastCron[a.a.GetId()].Equal(minute) {
					r.lastCron[a.a.GetId()] = minute
					due = true
				}
			} else if every := time.Duration(t.GetEverySeconds()) * time.Second; every > 0 {
				if now.Sub(r.lastRun[a.a.GetId()]) >= every {
					due = true
				}
			}
			if due {
				r.lastRun[a.a.GetId()] = now
			}
			r.mu.Unlock()
			if !due {
				continue
			}
			r.m.DB.PutBotSchedule(context.Background(), r.id, a.a.GetId(), now.UTC().Format(time.RFC3339Nano))
			if !roll(chanceOf(a.a)) {
				continue
			}
			sess, err := r.anySession()
			if err != nil {
				continue
			}
			p := r.persona(a.a.GetPersonaId())
			if a.a.GetAction().GetKind() == v1.ActionKind_ACTION_KIND_PRESENCE {
				go r.fire(sess, a, p, nil, triggerData{Now: now.Format(time.RFC1123), Persona: p.GetName(), Bot: r.name})
				continue
			}
			for _, channelID := range a.a.GetChannelIds() {
				data := triggerData{ChannelID: channelID, Now: now.Format(time.RFC1123), Persona: p.GetName(), Bot: r.name}
				if c, err := sess.Channel(channelID); err == nil {
					data.Channel, data.GuildID = c.Name, c.GuildID
					if g, err := sess.Guild(c.GuildID); err == nil {
						data.Guild = g.Name
					}
				}
				go r.fire(sess, a, p, &discordgo.Message{ChannelID: channelID, GuildID: data.GuildID}, data)
			}
		}
	}
}

// A chance of zero means always
func chanceOf(a *v1.Automation) float64 {
	if a.GetChance() == 0 {
		return 1
	}
	return a.GetChance()
}

// Fires the automations a message triggers
func (r *runner) messageAutomations(sess session, m *discordgo.Message, content string) {
	e := r.spec.GetEngagement()
	mentioned := false
	for _, u := range m.Mentions {
		if u.ID == r.selfID() {
			mentioned = true
		}
	}
	for _, a := range r.automata {
		t := a.a.GetTrigger()
		data := r.dataOf(sess, m, content)
		switch t.GetKind() {
		case v1.TriggerKind_TRIGGER_KIND_MESSAGE:
		case v1.TriggerKind_TRIGGER_KIND_MENTION:
			if !mentioned {
				continue
			}
		case v1.TriggerKind_TRIGGER_KIND_KEYWORD:
			if a.pattern == nil {
				continue
			}
			match := a.pattern.FindStringSubmatch(content)
			if match == nil {
				continue
			}
			data.Match = match
		case v1.TriggerKind_TRIGGER_KIND_COMMAND:
			if e.GetPrefix() == "" || !strings.HasPrefix(content, e.GetPrefix()) {
				continue
			}
			word, rest, _ := strings.Cut(strings.TrimPrefix(content, e.GetPrefix()), " ")
			if !strings.EqualFold(word, t.GetCommand()) {
				continue
			}
			data.Content = strings.TrimSpace(rest)
		default:
			continue
		}
		if !r.automationApplies(a, m.GuildID, m.ChannelID) {
			continue
		}
		p := r.persona(a.a.GetPersonaId())
		data.Persona = p.GetName()
		go r.fire(sess, a, p, m, data)
	}
}

// Runs join automations in their configured channels or the guild's system channel.
func (r *runner) onMemberAdd(shardID int, member *discordgo.Member) {
	sess := r.sessionOf(shardID)
	if sess == nil || member == nil || member.User == nil {
		return
	}
	for _, a := range r.automata {
		if a.a.GetTrigger().GetKind() != v1.TriggerKind_TRIGGER_KIND_MEMBER_JOIN || !r.automationApplies(a, member.GuildID, "") {
			continue
		}
		channels := a.a.GetChannelIds()
		guildName := ""
		if g, err := sess.Guild(member.GuildID); err == nil {
			guildName = g.Name
			if len(channels) == 0 && g.SystemChannelID != "" {
				channels = []string{g.SystemChannelID}
			}
		}
		if len(channels) == 0 {
			r.activity("warn", "automation", a.a.GetName()+": the guild has no system channel and the automation names none", member.GuildID, "", "", "")
			continue
		}
		p := r.persona(a.a.GetPersonaId())
		for _, channelID := range channels {
			data := triggerData{Content: member.User.DisplayName(), Author: member.User.DisplayName(), AuthorID: member.User.ID, ChannelID: channelID, Guild: guildName, GuildID: member.GuildID, Now: time.Now().Format(time.RFC1123), Persona: p.GetName(), Bot: r.name}
			if c, err := sess.Channel(channelID); err == nil {
				data.Channel = c.Name
			}
			go r.fire(sess, a, p, &discordgo.Message{ChannelID: channelID, GuildID: member.GuildID, Author: member.User}, data)
		}
	}
}

// Runs reaction automations on the reacted message.
func (r *runner) onReaction(shardID int, ev *discordgo.MessageReactionAdd) {
	sess := r.sessionOf(shardID)
	if sess == nil || ev == nil || ev.UserID == r.selfID() {
		return
	}
	for _, a := range r.automata {
		t := a.a.GetTrigger()
		if t.GetKind() != v1.TriggerKind_TRIGGER_KIND_REACTION || !sameEmoji(t.GetPattern(), &ev.Emoji) || !r.automationApplies(a, ev.GuildID, ev.ChannelID) {
			continue
		}
		p := r.persona(a.a.GetPersonaId())
		target := &discordgo.Message{ID: ev.MessageID, ChannelID: ev.ChannelID, GuildID: ev.GuildID}
		data := triggerData{Emoji: emojiName(&ev.Emoji), Content: emojiName(&ev.Emoji), ChannelID: ev.ChannelID, GuildID: ev.GuildID, AuthorID: ev.UserID, Now: time.Now().Format(time.RFC1123), Persona: p.GetName(), Bot: r.name}
		if ev.Member != nil && ev.Member.User != nil {
			data.Author = ev.Member.User.DisplayName()
			target.Author = ev.Member.User
		}
		if c, err := sess.Channel(ev.ChannelID); err == nil {
			data.Channel = c.Name
		}
		if g, err := sess.Guild(ev.GuildID); err == nil {
			data.Guild = g.Name
		}
		go r.fire(sess, a, p, target, data)
	}
}

// Whether an automation listens in this guild and channel
func (r *runner) automationApplies(a *automaton, guildID, channelID string) bool {
	if len(a.a.GetGuildIds()) > 0 && !contains(a.a.GetGuildIds(), guildID) {
		return false
	}
	if channelID != "" && len(a.a.GetChannelIds()) > 0 && !contains(a.a.GetChannelIds(), channelID) {
		return false
	}
	return true
}

// Template data for a message trigger.
func (r *runner) dataOf(sess session, m *discordgo.Message, content string) triggerData {
	data := triggerData{Content: content, ChannelID: m.ChannelID, GuildID: m.GuildID, Now: time.Now().Format(time.RFC1123), Bot: r.name}
	if m.Author != nil {
		data.Author, data.AuthorID = displayName(m), m.Author.ID
	}
	if c, err := sess.Channel(m.ChannelID); err == nil {
		data.Channel = c.Name
	}
	if m.GuildID != "" {
		if g, err := sess.Guild(m.GuildID); err == nil {
			data.Guild = g.Name
		}
	}
	return data
}

// Renders the action's template over the trigger
func (a *automaton) render(data triggerData) (string, error) {
	if a.tmpl == nil {
		return "", nil
	}
	var b bytes.Buffer
	if err := a.tmpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("template: %w", err)
	}
	return strings.TrimSpace(b.String()), nil
}

// Runs an automation after checking chance and cooldown. Scheduled automations
// have already passed those checks.
func (r *runner) fire(sess session, a *automaton, p *v1.Persona, m *discordgo.Message, data triggerData) {
	auto := a.a
	if auto.GetTrigger().GetKind() != v1.TriggerKind_TRIGGER_KIND_SCHEDULE {
		if !roll(chanceOf(auto)) {
			return
		}
		r.mu.Lock()
		last := r.lastRun[auto.GetId()]
		cool := time.Duration(auto.GetCooldownMs()) * time.Millisecond
		if cool > 0 && time.Since(last) < cool {
			r.mu.Unlock()
			return
		}
		r.lastRun[auto.GetId()] = time.Now()
		r.mu.Unlock()
	}
	select {
	case r.sem <- struct{}{}:
	case <-r.ctx.Done():
		return
	}
	defer func() { <-r.sem }()
	ctx, cancel := context.WithTimeout(r.ctx, generationTimeout)
	defer cancel()
	act := auto.GetAction()
	channelID, guildID := "", ""
	if m != nil {
		channelID, guildID = m.ChannelID, m.GuildID
	}
	failed := func(err error, trace string) {
		r.count(func(s *v1.BotStatus) { s.Errors++ })
		r.activity("error", "automation", auto.GetName()+": "+err.Error(), guildID, channelID, p.GetName(), trace)
	}
	text, err := a.render(data)
	if err != nil {
		failed(err, "")
		return
	}
	var ref *discordgo.MessageReference
	if act.GetReply() && m != nil && m.ID != "" {
		ref = &discordgo.MessageReference{MessageID: m.ID, ChannelID: m.ChannelID, GuildID: m.GuildID}
	}
	model := func(kind string) (string, error) {
		if act.GetModel() != "" {
			return act.GetModel(), nil
		}
		return r.route(p, kind)
	}
	switch act.GetKind() {
	case v1.ActionKind_ACTION_KIND_TEXT:
		if _, err := r.speak(sess, p, channelID, text, nil, ref); err != nil {
			failed(err, "")
			return
		}
		r.activity("info", "automation", auto.GetName()+": posted text", guildID, channelID, p.GetName(), "")
	case v1.ActionKind_ACTION_KIND_REACT:
		if m == nil || m.ID == "" {
			return
		}
		if err := sess.React(channelID, m.ID, act.GetEmoji()); err != nil {
			failed(fmt.Errorf("reaction failed: %s", describeREST(err)), "")
			return
		}
		r.activity("info", "automation", auto.GetName()+": reacted with "+act.GetEmoji(), guildID, channelID, p.GetName(), "")
	case v1.ActionKind_ACTION_KIND_PRESENCE:
		if err := r.setActivity(text); err != nil {
			failed(err, "")
			return
		}
		r.activity("info", "automation", auto.GetName()+": presence set to "+text, "", "", "", "")
	case v1.ActionKind_ACTION_KIND_CHAT:
		name, err := model("chat")
		if err != nil {
			failed(err, "")
			return
		}
		stop := r.typing(ctx, sess, channelID)
		chat := r.freshChat(p, sess, channelID, text)
		ans, err := r.chat(ctx, p, name, chat, nil)
		stop()
		trace := ""
		if ans != nil {
			trace = ans.trace
		}
		if err != nil {
			failed(err, trace)
			return
		}
		out := r.style(p, ans.text)
		if strings.TrimSpace(out) == "" {
			failed(fmt.Errorf("the model answered with nothing"), trace)
			return
		}
		if !r.wait(ctx, preDelay(p.GetHumanize())+typingTime(p.GetHumanize(), out)) {
			return
		}
		if _, err := r.speak(sess, p, channelID, out, nil, ref); err != nil {
			failed(err, trace)
			return
		}
		r.count(func(s *v1.BotStatus) { s.Replies++ })
		r.activity("info", "automation", auto.GetName()+": posted an answer", guildID, channelID, p.GetName(), trace)
	case v1.ActionKind_ACTION_KIND_IMAGE:
		name, err := model("image")
		if err != nil {
			failed(err, "")
			return
		}
		stop := r.typing(ctx, sess, channelID)
		files, trace, err := r.images(ctx, p, name, text, "", 0)
		stop()
		if err != nil {
			failed(err, trace)
			return
		}
		if _, err := r.speak(sess, p, channelID, "", files, ref); err != nil {
			failed(err, trace)
			return
		}
		r.count(func(s *v1.BotStatus) { s.Images += uint64(len(files)) })
		r.activity("info", "automation", fmt.Sprintf("%s: posted %d images", auto.GetName(), len(files)), guildID, channelID, p.GetName(), trace)
	case v1.ActionKind_ACTION_KIND_VIDEO:
		name, err := model("video")
		if err != nil {
			failed(err, "")
			return
		}
		stop := r.typing(ctx, sess, channelID)
		f, trace, err := r.video(ctx, p, name, text, "", nil)
		stop()
		if err != nil {
			failed(err, trace)
			return
		}
		if err := r.fits(f); err != nil {
			failed(err, trace)
			return
		}
		if _, err := r.speak(sess, p, channelID, "", []file{*f}, ref); err != nil {
			failed(err, trace)
			return
		}
		r.count(func(s *v1.BotStatus) { s.Videos++ })
		r.activity("info", "automation", auto.GetName()+": posted a video", guildID, channelID, p.GetName(), trace)
	}
}
