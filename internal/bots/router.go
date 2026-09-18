package bots

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/gateway"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

var mentionPattern = regexp.MustCompile(`<@!?(\d+)>`)

func (r *runner) selfID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.self == nil {
		return ""
	}
	return r.self.ID
}

// Decides what one message gets: nothing, a reaction, an answer from a persona, a command, or an automation
func (r *runner) onMessage(shardID int, m *discordgo.Message) {
	if m == nil || m.Author == nil || m.Author.ID == r.selfID() || r.selfID() == "" {
		return
	}
	sess := r.sessionOf(shardID)
	if sess == nil {
		return
	}
	e := r.spec.GetEngagement()
	dm := m.GuildID == ""
	if dm && !e.GetDirectMessages() {
		return
	}
	if !r.allowed(sess, m) {
		return
	}
	fromBot := m.Author.Bot || m.WebhookID != ""
	if fromBot && !e.GetAnswerBots() {
		return
	}
	content := r.clean(m)
	go r.messageAutomations(sess, m, content)
	if e.GetPrefix() != "" && strings.HasPrefix(content, e.GetPrefix()) {
		go r.prefixCommand(sess, m, strings.TrimSpace(strings.TrimPrefix(content, e.GetPrefix())))
		return
	}
	p, addressed := r.pick(m, content)
	h := p.GetHumanize()
	if !addressed && e.GetRequireMention() && !dm && !roll(h.GetAmbientReplyChance()) {
		return
	}
	if !awake(h, time.Now()) {
		return
	}
	if addressed && roll(h.GetIgnoreChance()) {
		r.activity("info", "reply", "left a message unanswered by chance", m.GuildID, m.ChannelID, p.GetName(), "")
		return
	}
	if fromBot && r.chainTooLong(sess, m) {
		return
	}
	if !r.cooldownOK(m) {
		return
	}
	go r.reply(sess, m, p, content)
}

// Whether the bot may answer in this guild, channel, and to this person
func (r *runner) allowed(sess session, m *discordgo.Message) bool {
	e := r.spec.GetEngagement()
	if m.GuildID != "" && len(e.GetGuildIds()) > 0 && !contains(e.GetGuildIds(), m.GuildID) {
		return false
	}
	if len(e.GetUserIds()) > 0 && !contains(e.GetUserIds(), m.Author.ID) {
		return false
	}
	if contains(e.GetDeniedUserIds(), m.Author.ID) {
		return false
	}
	if len(e.GetChannelIds()) == 0 && len(e.GetDeniedChannelIds()) == 0 {
		return true
	}
	ids := []string{m.ChannelID}
	if parent := r.parentOf(sess, m.ChannelID); parent != "" {
		ids = append(ids, parent)
	}
	for _, id := range ids {
		if contains(e.GetDeniedChannelIds(), id) {
			return false
		}
	}
	if len(e.GetChannelIds()) == 0 || m.GuildID == "" {
		return true
	}
	for _, id := range ids {
		if contains(e.GetChannelIds(), id) {
			return true
		}
	}
	return false
}

// The parent of a thread, empty for a channel that is not one
func (r *runner) parentOf(sess session, channelID string) string {
	c, err := sess.Channel(channelID)
	if err != nil || !c.IsThread() {
		return ""
	}
	return c.ParentID
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// The content with mentions of the bot removed and others turned into names
func (r *runner) clean(m *discordgo.Message) string {
	self := r.selfID()
	names := map[string]string{}
	for _, u := range m.Mentions {
		names[u.ID] = "@" + u.DisplayName()
	}
	out := mentionPattern.ReplaceAllStringFunc(m.Content, func(s string) string {
		id := mentionPattern.FindStringSubmatch(s)[1]
		if id == self {
			return ""
		}
		if n, ok := names[id]; ok {
			return n
		}
		return s
	})
	return strings.TrimSpace(strings.Join(strings.Fields(out), " "))
}

// The persona a message goes to and whether it was addressed: a mention, a reply to the bot, a direct message, or a wake word
func (r *runner) pick(m *discordgo.Message, content string) (*v1.Persona, bool) {
	c := r.channel(m.ChannelID)
	r.mu.Lock()
	chosen := c.row.PersonaID
	r.mu.Unlock()
	p := r.persona(chosen)
	if chosen == "" {
		for _, cand := range r.spec.GetPersonas() {
			if contains(cand.GetChannelIds(), m.ChannelID) {
				p = cand
				break
			}
		}
	}
	lower := strings.ToLower(content)
	for _, cand := range r.spec.GetPersonas() {
		for _, w := range append([]string{cand.GetName()}, cand.GetWakeWords()...) {
			if hasWord(lower, strings.ToLower(w)) {
				return cand, true
			}
		}
	}
	self := r.selfID()
	for _, u := range m.Mentions {
		if u.ID == self {
			return p, true
		}
	}
	if ref := m.ReferencedMessage; ref != nil && ref.Author != nil && (ref.Author.ID == self || r.ownWebhook(ref)) {
		return p, true
	}
	return p, m.GuildID == ""
}

// Whether a word stands whole in the text
func hasWord(text, word string) bool {
	if word == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(text[i:], word)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(word)
		before := start == 0 || !isWordByte(text[start-1])
		after := end == len(text) || !isWordByte(text[end])
		if before && after {
			return true
		}
		i = start + 1
		if i >= len(text) {
			return false
		}
	}
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b >= 0x80
}

// Whether a message came through one of this bot's persona webhooks
func (r *runner) ownWebhook(m *discordgo.Message) bool {
	if m.WebhookID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.channels {
		if c.row.WebhookID == m.WebhookID {
			return true
		}
	}
	return false
}

// Whether the messages before this one are a run of bots as long as the bot tolerates
func (r *runner) chainTooLong(sess session, m *discordgo.Message) bool {
	limit := int(r.spec.GetEngagement().GetMaxBotChain())
	before, err := sess.Messages(m.ChannelID, limit-1, m.ID)
	if err != nil {
		return false
	}
	run := 1
	for _, prev := range before {
		if prev.Author == nil || !(prev.Author.Bot || prev.WebhookID != "") {
			break
		}
		run++
	}
	if run >= limit {
		r.activity("info", "reply", fmt.Sprintf("stayed quiet after %d bot messages in a row", run), m.GuildID, m.ChannelID, "", "")
		return true
	}
	return false
}

// Whether the channel and the person are past their cooldowns
func (r *runner) cooldownOK(m *discordgo.Message) bool {
	e := r.spec.GetEngagement()
	c := r.channel(m.ChannelID)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if d := time.Duration(e.GetCooldownMs()) * time.Millisecond; d > 0 && now.Sub(c.lastReply) < d {
		return false
	}
	if d := time.Duration(e.GetUserCooldownMs()) * time.Millisecond; d > 0 && now.Sub(r.users[m.Author.ID]) < d {
		return false
	}
	return true
}

func (r *runner) markReplied(m *discordgo.Message) {
	c := r.channel(m.ChannelID)
	r.mu.Lock()
	c.lastReply = time.Now()
	r.users[m.Author.ID] = c.lastReply
	r.mu.Unlock()
}

// Takes the channel for one answer, false when one is already being made
func (r *runner) claim(channelID string) bool {
	c := r.channel(channelID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.busy {
		return false
	}
	c.busy = true
	return true
}

func (r *runner) release(channelID string) {
	c := r.channel(channelID)
	r.mu.Lock()
	c.busy = false
	r.mu.Unlock()
}

// Keeps the typing indicator up until the returned function is called
func (r *runner) typing(ctx context.Context, sess session, channelID string) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		for {
			sess.Typing(channelID)
			select {
			case <-ctx.Done():
				return
			case <-time.After(typingRenew):
			}
		}
	}()
	return cancel
}

// Waits d or until the bot stops
func (r *runner) wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// Answers a message as a persona, the way that persona is set to answer
func (r *runner) reply(sess session, m *discordgo.Message, p *v1.Persona, content string) {
	if !r.claim(m.ChannelID) {
		return
	}
	defer r.release(m.ChannelID)
	select {
	case r.sem <- struct{}{}:
	case <-r.ctx.Done():
		return
	}
	defer func() { <-r.sem }()
	ctx, cancel := context.WithTimeout(r.ctx, generationTimeout)
	defer cancel()
	h := p.GetHumanize()
	if h.GetEnabled() && roll(h.GetReactionChance()) {
		if emoji := pick(h.GetReactions()); emoji != "" {
			if err := sess.React(m.ChannelID, m.ID, emoji); err != nil {
				r.activity("warn", "reaction", "reaction failed: "+describeREST(err), m.GuildID, m.ChannelID, p.GetName(), "")
			} else {
				r.activity("info", "reaction", "reacted with "+emoji, m.GuildID, m.ChannelID, p.GetName(), "")
			}
		}
	}
	model, err := r.route(p, "chat")
	if err != nil {
		r.problem(sess, m, p, err, "")
		return
	}
	chat, err := r.buildChat(ctx, sess, p, m, content)
	if err != nil {
		r.problem(sess, m, p, err, "")
		return
	}
	if !r.wait(ctx, preDelay(h)) {
		return
	}
	stopTyping := r.typing(ctx, sess, m.ChannelID)
	defer stopTyping()
	speaker, err := r.speaker(sess, p, m.ChannelID)
	if err != nil {
		r.problem(sess, m, p, err, "")
		return
	}
	var ref *discordgo.MessageReference
	if h.GetEnabled() && h.GetQuoteReply() {
		ref = &discordgo.MessageReference{MessageID: m.ID, ChannelID: m.ChannelID, GuildID: m.GuildID}
		speaker.quoteAuthor = m.Author.ID
	}
	if p.GetStream() {
		r.streamed(ctx, speaker, m, p, model, chat, ref)
		return
	}
	start := time.Now()
	ans, err := r.chat(ctx, p, model, chat, nil)
	if err != nil {
		trace := ""
		if ans != nil {
			trace = ans.trace
		}
		r.problem(sess, m, p, err, trace)
		return
	}
	text := r.style(p, ans.text)
	if strings.TrimSpace(text) == "" {
		r.activity("warn", "reply", "the model answered with nothing", m.GuildID, m.ChannelID, p.GetName(), ans.trace)
		return
	}
	var chunks []string
	if h.GetEnabled() && h.GetSplitMessages() {
		chunks = split(text, int(h.GetMaxChunkChars()))
	} else {
		chunks = chunk(text, discordMessageMax)
	}
	for i, piece := range chunks {
		// Each message a person sends ends the casual way, not only the last
		if h.GetEnabled() && h.GetCasual() {
			piece = casual(piece)
		}
		d := typingTime(h, piece)
		if i == 0 {
			d -= time.Since(start)
		}
		if !r.wait(ctx, d) {
			return
		}
		var reference *discordgo.MessageReference
		if i == 0 {
			reference = ref
		}
		if _, err := speaker.send(piece, nil, reference); err != nil {
			r.problem(sess, m, p, fmt.Errorf("message not sent: %s", describeREST(err)), ans.trace)
			return
		}
	}
	r.markReplied(m)
	r.count(func(s *v1.BotStatus) { s.Replies++ })
	r.activity("info", "reply", fmt.Sprintf("answered %s in %d messages", m.Author.DisplayName(), len(chunks)), m.GuildID, m.ChannelID, p.GetName(), ans.trace)
}

// Answers by posting once and editing as the text streams in, the rest posted after when it runs past one message
func (r *runner) streamed(ctx context.Context, sp *speaker, m *discordgo.Message, p *v1.Persona, model string, chat *gateway.Chat, ref *discordgo.MessageReference) {
	var mu sync.Mutex
	var buf strings.Builder
	var first *discordgo.Message
	var sendErr error
	shown := ""
	flush := func() {
		mu.Lock()
		text := buf.String()
		mu.Unlock()
		if sendErr != nil || strings.TrimSpace(text) == "" || text == shown {
			return
		}
		head := text
		if len(head) > discordMessageMax {
			head = head[:discordMessageMax]
		}
		if first == nil {
			first, sendErr = sp.send(head, nil, ref)
		} else {
			sendErr = sp.edit(first.ID, head)
		}
		shown = text
	}
	stop := make(chan struct{})
	ticks := make(chan struct{})
	go func() {
		defer close(ticks)
		for {
			select {
			case <-stop:
				return
			case <-time.After(streamEdit):
				flush()
			}
		}
	}()
	ans, err := r.chat(ctx, p, model, chat, func(delta string) {
		mu.Lock()
		buf.WriteString(delta)
		mu.Unlock()
	})
	close(stop)
	<-ticks
	if err != nil {
		trace := ""
		if ans != nil {
			trace = ans.trace
		}
		if first != nil {
			sp.edit(first.ID, strings.TrimSpace(shown)+"\n\n⚠️ "+err.Error())
		}
		r.problem(sp.sess, m, p, err, trace)
		return
	}
	text := r.style(p, ans.text)
	if strings.TrimSpace(text) == "" {
		r.activity("warn", "reply", "the model answered with nothing", m.GuildID, m.ChannelID, p.GetName(), ans.trace)
		return
	}
	chunks := chunk(text, discordMessageMax)
	if first == nil {
		first, sendErr = sp.send(chunks[0], nil, ref)
	} else {
		sendErr = sp.edit(first.ID, chunks[0])
	}
	for _, piece := range chunks[1:] {
		if sendErr != nil {
			break
		}
		_, sendErr = sp.send(piece, nil, nil)
	}
	if sendErr != nil {
		r.problem(sp.sess, m, p, fmt.Errorf("message not sent: %s", describeREST(sendErr)), ans.trace)
		return
	}
	r.markReplied(m)
	r.count(func(s *v1.BotStatus) { s.Replies++ })
	r.activity("info", "reply", fmt.Sprintf("answered %s, streamed", m.Author.DisplayName()), m.GuildID, m.ChannelID, p.GetName(), ans.trace)
}

// Records a failed answer; a persona that passes for a person keeps quiet, any other says what went wrong
func (r *runner) problem(sess session, m *discordgo.Message, p *v1.Persona, err error, trace string) {
	r.count(func(s *v1.BotStatus) { s.Errors++ })
	if p.GetHumanize().GetEnabled() {
		r.activity("error", "reply", "answer failed, kept quiet as a person would: "+err.Error(), m.GuildID, m.ChannelID, p.GetName(), trace)
		return
	}
	r.activity("error", "reply", "answer failed: "+err.Error(), m.GuildID, m.ChannelID, p.GetName(), trace)
	sess.SendMessage(m.ChannelID, &discordgo.MessageSend{Content: "⚠️ " + err.Error(), Reference: &discordgo.MessageReference{MessageID: m.ID, ChannelID: m.ChannelID, GuildID: m.GuildID}, AllowedMentions: &discordgo.MessageAllowedMentions{}})
}

// Shapes an answer the persona's way: no echoed name prefix, casual when asked
func (r *runner) style(p *v1.Persona, text string) string {
	text = strings.TrimSpace(text)
	prefix := p.GetName() + ":"
	if len(text) > len(prefix) && strings.EqualFold(text[:len(prefix)], prefix) {
		text = strings.TrimSpace(text[len(prefix):])
	}
	if h := p.GetHumanize(); h.GetEnabled() && h.GetCasual() {
		text = casual(text)
	}
	return text
}

// One turn as the channel history gives it, before roles are merged
type turn struct {
	role  string
	text  string
	parts []gateway.Part
}

// Reads the channel back into a chat the persona answers: its prompt, the setting, the history, and the message
func (r *runner) buildChat(ctx context.Context, sess session, p *v1.Persona, m *discordgo.Message, content string) (*gateway.Chat, error) {
	mem := r.spec.GetMemory()
	c := r.channel(m.ChannelID)
	r.mu.Lock()
	cutoff, hook := c.row.Cutoff, c.row.WebhookID
	r.mu.Unlock()
	if parent := r.parentOf(sess, m.ChannelID); parent != "" {
		pc := r.channel(parent)
		r.mu.Lock()
		if hook == "" {
			hook = pc.row.WebhookID
		}
		r.mu.Unlock()
	}
	history, err := sess.Messages(m.ChannelID, int(mem.GetMessages()), m.ID)
	if err != nil {
		return nil, fmt.Errorf("channel history could not be read: %s", describeREST(err))
	}
	self := r.selfID()
	var turns []turn
	oldest := time.Time{}
	if w := mem.GetWindowMinutes(); w > 0 {
		oldest = time.Now().Add(-time.Duration(w) * time.Minute)
	}
	for i := len(history) - 1; i >= 0; i-- {
		h := history[i]
		if h.Author == nil || (cutoff != "" && !newer(h.ID, cutoff)) || (!oldest.IsZero() && h.Timestamp.Before(oldest)) {
			continue
		}
		mine := h.Author.ID == self || (h.WebhookID != "" && h.WebhookID == hook && strings.EqualFold(h.Author.Username, p.GetName()))
		text := r.clean(h)
		for _, a := range h.Attachments {
			text = strings.TrimSpace(text + " [" + attachmentWord(a) + ": " + a.Filename + "]")
		}
		if text == "" {
			continue
		}
		if mine {
			turns = append(turns, turn{role: "assistant", text: text})
			continue
		}
		turns = append(turns, turn{role: "user", text: r.named(h, text)})
	}
	last := turn{role: "user", text: r.named(m, content)}
	if p.GetVision() {
		parts, notes, err := r.attachmentParts(ctx, m)
		if err != nil {
			return nil, err
		}
		last.parts = parts
		if len(notes) > 0 {
			last.text = strings.TrimSpace(last.text + " " + strings.Join(notes, " "))
		}
	} else {
		for _, a := range m.Attachments {
			last.text = strings.TrimSpace(last.text + " [" + attachmentWord(a) + ": " + a.Filename + "]")
		}
	}
	if last.text == "" && len(last.parts) == 0 {
		last.text = "(an empty message)"
	}
	turns = append(turns, last)
	turns = trimTurns(mergeTurns(turns), int(mem.GetMaxChars()))
	chat := &gateway.Chat{Messages: []gateway.Message{{Role: "system", Parts: []gateway.Part{{Type: "text", Text: r.systemPrompt(sess, p, m)}}}}}
	for _, t := range turns {
		msg := gateway.Message{Role: t.role, Parts: append([]gateway.Part(nil), t.parts...)}
		if t.text != "" {
			msg.Parts = append(msg.Parts, gateway.Part{Type: "text", Text: t.text})
		}
		chat.Messages = append(chat.Messages, msg)
	}
	return chat, nil
}

// A chat of the prompt, the setting, and one message, for posts made by hand or on a schedule
func (r *runner) freshChat(p *v1.Persona, sess session, channelID, content string) *gateway.Chat {
	m := &discordgo.Message{ChannelID: channelID}
	if c, err := sess.Channel(channelID); err == nil {
		m.GuildID = c.GuildID
	}
	return &gateway.Chat{Messages: []gateway.Message{
		{Role: "system", Parts: []gateway.Part{{Type: "text", Text: r.systemPrompt(sess, p, m)}}},
		{Role: "user", Parts: []gateway.Part{{Type: "text", Text: content}}},
	}}
}

func attachmentWord(a *discordgo.MessageAttachment) string {
	switch {
	case isImage(a):
		return "image"
	case isVideo(a):
		return "video"
	}
	return "attachment"
}

// A message's text with its author's name in front, when names are kept
func (r *runner) named(m *discordgo.Message, text string) string {
	if !r.spec.GetMemory().GetIncludeNames() || m.Author == nil {
		return text
	}
	return displayName(m) + ": " + text
}

func displayName(m *discordgo.Message) string {
	if m.Member != nil && m.Member.Nick != "" {
		return m.Member.Nick
	}
	if m.Author == nil {
		return "someone"
	}
	return m.Author.DisplayName()
}

// Joins turns of one role that follow each other, since chat templates expect roles to alternate
func mergeTurns(turns []turn) []turn {
	var out []turn
	for _, t := range turns {
		if n := len(out); n > 0 && out[n-1].role == t.role {
			if out[n-1].text != "" && t.text != "" {
				out[n-1].text += "\n"
			}
			out[n-1].text += t.text
			out[n-1].parts = append(out[n-1].parts, t.parts...)
			continue
		}
		out = append(out, t)
	}
	return out
}

// Drops the oldest turns until the text fits, the last turn always kept
func trimTurns(turns []turn, maxChars int) []turn {
	if maxChars <= 0 || len(turns) == 0 {
		return turns
	}
	total := 0
	for _, t := range turns {
		total += len(t.text)
	}
	for len(turns) > 1 && total > maxChars {
		total -= len(turns[0].text)
		turns = turns[1:]
	}
	return turns
}

// The persona's prompt with where it is and when
func (r *runner) systemPrompt(sess session, p *v1.Persona, m *discordgo.Message) string {
	var b bytes.Buffer
	if prompt := strings.TrimSpace(p.GetSystemPrompt()); prompt != "" {
		b.WriteString(prompt)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "Your name is %s. ", p.GetName())
	if m.GuildID == "" {
		who := "someone"
		if m.Author != nil {
			who = displayName(m)
		}
		fmt.Fprintf(&b, "You are in a Discord direct message with %s. ", who)
	} else {
		guild, channel := "", ""
		if g, err := sess.Guild(m.GuildID); err == nil {
			guild = g.Name
		}
		if c, err := sess.Channel(m.ChannelID); err == nil {
			channel = c.Name
		}
		switch {
		case guild != "" && channel != "":
			fmt.Fprintf(&b, "You are in the Discord server %q, in #%s. ", guild, channel)
		case channel != "":
			fmt.Fprintf(&b, "You are in the Discord channel #%s. ", channel)
		default:
			b.WriteString("You are in a Discord server. ")
		}
	}
	now := time.Now()
	if tz := p.GetHumanize().GetTimezone(); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			now = now.In(loc)
		}
	}
	fmt.Fprintf(&b, "The time is %s. ", now.Format("Monday, January 2 2006, 15:04 MST"))
	if r.spec.GetMemory().GetIncludeNames() {
		b.WriteString("Messages from other people start with their name and a colon; never start your own message with a name, and answer as yourself.")
	}
	return strings.TrimSpace(b.String())
}

// Posts through the bot itself or through a channel webhook wearing the persona's name and face
type speaker struct {
	r        *runner
	sess     session
	p        *v1.Persona
	channel  string
	threadID string
	hook     *db.BotChannel
	// Who a webhook persona names when asked to reply, since a webhook cannot quote
	quoteAuthor string
}

// The way to speak in a channel as a persona, the webhook found or made when the persona uses one
func (r *runner) speaker(sess session, p *v1.Persona, channelID string) (*speaker, error) {
	sp := &speaker{r: r, sess: sess, p: p, channel: channelID}
	if !p.GetWebhook() {
		return sp, nil
	}
	target := channelID
	if parent := r.parentOf(sess, channelID); parent != "" {
		sp.threadID, target = channelID, parent
	}
	hook, err := r.webhook(sess, target)
	if err != nil {
		return nil, err
	}
	sp.hook = hook
	return sp, nil
}

// The bot's webhook on a channel, from the channel row, the channel's own list, or made new
func (r *runner) webhook(sess session, channelID string) (*db.BotChannel, error) {
	c := r.channel(channelID)
	r.mu.Lock()
	row := c.row
	r.mu.Unlock()
	if row.WebhookID != "" && row.WebhookToken != "" {
		return &row, nil
	}
	name := webhookPrefix + r.name
	hooks, err := sess.Webhooks(channelID)
	if err != nil {
		if missingPermission(err) {
			return nil, fmt.Errorf("persona %s speaks through a webhook, which needs the Manage Webhooks permission in this channel", r.name)
		}
		return nil, fmt.Errorf("webhooks could not be listed: %s", describeREST(err))
	}
	var found *discordgo.Webhook
	for _, h := range hooks {
		if h.Name == name && h.Token != "" {
			found = h
			break
		}
	}
	if found == nil {
		found, err = sess.CreateWebhook(channelID, name)
		if err != nil {
			if missingPermission(err) {
				return nil, fmt.Errorf("persona webhooks need the Manage Webhooks permission in this channel")
			}
			return nil, fmt.Errorf("webhook not created: %s", describeREST(err))
		}
	}
	r.mu.Lock()
	c.row.WebhookID, c.row.WebhookToken = found.ID, found.Token
	row = c.row
	r.mu.Unlock()
	if err := r.saveChannel(c); err != nil {
		return nil, err
	}
	return &row, nil
}

// Forgets a webhook Discord no longer knows
func (r *runner) forgetWebhook(channelID string) {
	c := r.channel(channelID)
	r.mu.Lock()
	c.row.WebhookID, c.row.WebhookToken = "", ""
	r.mu.Unlock()
	r.saveChannel(c)
}

func (sp *speaker) send(text string, files []file, ref *discordgo.MessageReference) (*discordgo.Message, error) {
	var attached []*discordgo.File
	for _, f := range files {
		attached = append(attached, &discordgo.File{Name: f.name, ContentType: f.mime, Reader: bytes.NewReader(f.data)})
	}
	if sp.hook == nil {
		return sp.sess.SendMessage(sp.channel, &discordgo.MessageSend{Content: text, Files: attached, Reference: ref, AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{discordgo.AllowedMentionTypeUsers}}})
	}
	// A webhook cannot reply, so a quoted answer names the person instead
	if ref != nil && sp.quoteAuthor != "" {
		text = "<@" + sp.quoteAuthor + "> " + text
	}
	params := &discordgo.WebhookParams{Content: text, Username: sp.p.GetName(), AvatarURL: sp.p.GetAvatarUrl(), Files: attached, AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{discordgo.AllowedMentionTypeUsers}}}
	msg, err := sp.sess.ExecuteWebhook(sp.hook.WebhookID, sp.hook.WebhookToken, sp.threadID, params)
	if err != nil && unknownWebhook(err) {
		// The webhook was deleted on Discord's side; make another and try once more
		sp.r.forgetWebhook(sp.hook.ChannelID)
		hook, again := sp.r.webhook(sp.sess, sp.hook.ChannelID)
		if again != nil {
			return nil, again
		}
		sp.hook = hook
		for _, f := range attached {
			if s, ok := f.Reader.(*bytes.Reader); ok {
				s.Seek(0, 0)
			}
		}
		msg, err = sp.sess.ExecuteWebhook(sp.hook.WebhookID, sp.hook.WebhookToken, sp.threadID, params)
	}
	return msg, err
}

func (sp *speaker) edit(messageID, text string) error {
	if sp.hook == nil {
		_, err := sp.sess.EditMessage(sp.channel, messageID, text)
		return err
	}
	_, err := sp.sess.EditWebhookMessage(sp.hook.WebhookID, sp.hook.WebhookToken, messageID, &discordgo.WebhookEdit{Content: &text})
	return err
}

// Posts a persona's text and files in a channel, the way sendNow and automations post
func (r *runner) speak(sess session, p *v1.Persona, channelID, text string, files []file, ref *discordgo.MessageReference) (*discordgo.Message, error) {
	sp, err := r.speaker(sess, p, channelID)
	if err != nil {
		return nil, err
	}
	chunks := chunk(text, discordMessageMax)
	if len(chunks) == 0 {
		chunks = []string{""}
	}
	var first *discordgo.Message
	for i, piece := range chunks {
		var attached []file
		if i == len(chunks)-1 {
			attached = files
		}
		var reference *discordgo.MessageReference
		if i == 0 {
			reference = ref
		}
		msg, err := sp.send(piece, attached, reference)
		if err != nil {
			return first, fmt.Errorf("message not sent: %s", describeREST(err))
		}
		if first == nil {
			first = msg
		}
	}
	return first, nil
}
