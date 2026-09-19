package bots

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/bwmarrin/discordgo"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Slash commands with configured names.
func (r *runner) commandDefinitions() []*discordgo.ApplicationCommand {
	c := r.spec.GetCommands()
	name := func(role string) string { return c.GetNames()[role] }
	var out []*discordgo.ApplicationCommand
	add := func(role, description string, options ...*discordgo.ApplicationCommandOption) {
		if contains(c.GetDisabled(), role) {
			return
		}
		out = append(out, &discordgo.ApplicationCommand{Name: name(role), Description: description, Options: options})
	}
	persona := &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "persona", Description: "Which persona answers"}
	if len(r.spec.GetPersonas()) <= 25 {
		for _, p := range r.spec.GetPersonas() {
			persona.Choices = append(persona.Choices, &discordgo.ApplicationCommandOptionChoice{Name: p.GetName(), Value: p.GetId()})
		}
	}
	add("ask", "Ask the model a question",
		&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "What to ask", Required: true},
		&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionAttachment, Name: "image", Description: "An image to look at"},
		persona,
	)
	add("imagine", "Make an image from a prompt",
		&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "What to draw", Required: true},
		&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionAttachment, Name: "image", Description: "An image to start from"},
		&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionInteger, Name: "count", Description: "How many images", MinValue: ptr(1.0), MaxValue: maxImageCount},
		persona,
	)
	add("video", "Make a video from a prompt",
		&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "prompt", Description: "What happens", Required: true},
		&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionAttachment, Name: "image", Description: "An image or a video to start from"},
		persona,
	)
	add("persona", "See or set which persona speaks in this channel",
		&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "The persona to use here, none to see the list"},
	)
	add("models", "List the models the bot can reach")
	add("reset", "Forget this channel's conversation so far")
	add("help", "What this bot does")
	return out
}

func ptr[T any](v T) *T { return &v }

// Registers the slash commands in each named guild, or globally
func (r *runner) registerCommands(sess session) {
	if sess == nil {
		return
	}
	r.mu.Lock()
	appID := r.appID
	r.mu.Unlock()
	if appID == "" {
		appID = sess.ApplicationID()
	}
	if appID == "" {
		r.activity("error", "command", "commands not registered: Discord did not name the application", "", "", "", "")
		return
	}
	defs := r.commandDefinitions()
	targets := r.spec.GetCommands().GetGuildIds()
	if len(targets) == 0 {
		targets = []string{""}
	}
	for _, guildID := range targets {
		if err := sess.OverwriteCommands(appID, guildID, defs); err != nil {
			where := "globally"
			if guildID != "" {
				where = "in guild " + guildID
			}
			r.activity("error", "command", fmt.Sprintf("commands not registered %s: %s", where, describeREST(err)), guildID, "", "", "")
			continue
		}
		if guildID == "" {
			r.activity("info", "command", fmt.Sprintf("registered %d commands globally, which Discord rolls out within an hour", len(defs)), "", "", "", "")
		} else {
			r.activity("info", "command", fmt.Sprintf("registered %d commands in guild %s", len(defs), guildID), guildID, "", "", "")
		}
	}
}

// Resolves a command name to its role.
func (r *runner) roleOf(name string) string {
	for role, n := range r.spec.GetCommands().GetNames() {
		if n == name {
			return role
		}
	}
	return ""
}

// A slash or prefix command invocation.
type invocation struct {
	sess      session
	channelID string
	guildID   string
	user      *discordgo.User
	// The interaction to follow up on, nil for a prefix command
	interaction *discordgo.Interaction
	// The message to reply to, nil for a slash command
	message *discordgo.Message
	// Whether the answer is shown only to the caller, for slash commands
	private bool
}

// Answers a command with text and files
func (r *runner) respond(in *invocation, p *v1.Persona, text string, files []file) error {
	if in.interaction != nil {
		var attached []*discordgo.File
		for _, f := range files {
			attached = append(attached, &discordgo.File{Name: f.name, ContentType: f.mime, Reader: bytes.NewReader(f.data)})
		}
		chunks := chunk(text, discordMessageMax)
		if len(chunks) == 0 {
			chunks = []string{""}
		}
		for i, piece := range chunks {
			params := &discordgo.WebhookParams{Content: piece, AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{discordgo.AllowedMentionTypeUsers}}}
			if i == len(chunks)-1 {
				params.Files = attached
			}
			if in.private {
				params.Flags = discordgo.MessageFlagsEphemeral
			}
			if _, err := in.sess.Followup(in.interaction, params); err != nil {
				return fmt.Errorf("answer not sent: %s", describeREST(err))
			}
		}
		return nil
	}
	var ref *discordgo.MessageReference
	if in.message != nil {
		ref = &discordgo.MessageReference{MessageID: in.message.ID, ChannelID: in.message.ChannelID, GuildID: in.message.GuildID}
	}
	_, err := r.speak(in.sess, p, in.channelID, text, files, ref)
	return err
}

// Handles a slash command
func (r *runner) onInteraction(shardID int, i *discordgo.Interaction) {
	sess := r.sessionOf(shardID)
	if sess == nil || i == nil || i.Type != discordgo.InteractionApplicationCommand {
		return
	}
	data := i.ApplicationCommandData()
	role := r.roleOf(data.Name)
	if role == "" {
		return
	}
	user := i.User
	if i.Member != nil {
		user = i.Member.User
	}
	if user == nil {
		return
	}
	if !r.allowed(sess, &discordgo.Message{ChannelID: i.ChannelID, GuildID: i.GuildID, Author: user}) {
		sess.Respond(i, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource, Data: &discordgo.InteractionResponseData{Content: "This bot does not answer here.", Flags: discordgo.MessageFlagsEphemeral}})
		return
	}
	private := role == "persona" || role == "models" || role == "reset" || role == "help"
	var flags discordgo.MessageFlags
	if private {
		flags = discordgo.MessageFlagsEphemeral
	}
	if err := sess.Respond(i, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredChannelMessageWithSource, Data: &discordgo.InteractionResponseData{Flags: flags}}); err != nil {
		r.activity("error", "command", "interaction not acknowledged: "+describeREST(err), i.GuildID, i.ChannelID, "", "")
		return
	}
	args := map[string]*discordgo.ApplicationCommandInteractionDataOption{}
	for _, o := range data.Options {
		args[o.Name] = o
	}
	str := func(name string) string {
		if o, ok := args[name]; ok {
			return strings.TrimSpace(o.StringValue())
		}
		return ""
	}
	var attachment *discordgo.MessageAttachment
	if o, ok := args["image"]; ok && data.Resolved != nil {
		attachment = data.Resolved.Attachments[o.StringValue()]
	}
	count := 0
	if o, ok := args["count"]; ok {
		count = int(o.IntValue())
	}
	in := &invocation{sess: sess, channelID: i.ChannelID, guildID: i.GuildID, user: user, interaction: i, private: private}
	go r.command(in, role, str("prompt"), str("persona"), str("name"), attachment, count)
}

// Handles a prefix command such as !imagine
func (r *runner) prefixCommand(sess session, m *discordgo.Message, rest string) {
	word, args, _ := strings.Cut(rest, " ")
	role := r.roleOf(strings.ToLower(word))
	if role == "" || contains(r.spec.GetCommands().GetDisabled(), role) {
		// Allow automations to handle unrecognized commands.
		return
	}
	var attachment *discordgo.MessageAttachment
	if len(m.Attachments) > 0 {
		attachment = m.Attachments[0]
	}
	in := &invocation{sess: sess, channelID: m.ChannelID, guildID: m.GuildID, user: m.Author, message: m}
	args = strings.TrimSpace(args)
	if role == "persona" {
		r.command(in, role, "", "", args, attachment, 0)
		return
	}
	r.command(in, role, args, "", "", attachment, 0)
}

// Runs one command and answers it
func (r *runner) command(in *invocation, role, prompt, personaID, personaName string, attachment *discordgo.MessageAttachment, count int) {
	c := r.channel(in.channelID)
	r.mu.Lock()
	channelPersona := c.row.PersonaID
	r.mu.Unlock()
	p := r.persona(firstNonEmpty(personaID, channelPersona))
	// Resolve typed persona names when Discord's choice limit is exceeded.
	if _, known := r.personaBy[personaID]; personaID != "" && !known {
		if named, ok := r.personaNamed(personaID); ok {
			p = named
		}
	}
	who := "someone"
	if in.user != nil {
		who = in.user.DisplayName()
	}
	fail := func(err error, trace string) {
		r.count(func(s *v1.BotStatus) { s.Errors++ })
		r.activity("error", "command", role+" failed: "+err.Error(), in.guildID, in.channelID, p.GetName(), trace)
		r.respond(in, p, "⚠️ "+err.Error(), nil)
	}
	select {
	case r.sem <- struct{}{}:
	case <-r.ctx.Done():
		return
	}
	defer func() { <-r.sem }()
	ctx, cancel := context.WithTimeout(r.ctx, generationTimeout)
	defer cancel()
	switch role {
	case "help":
		r.respond(in, p, r.helpText(), nil)
	case "models":
		r.respond(in, p, r.modelsText(), nil)
	case "reset":
		newest, err := in.sess.Messages(in.channelID, 1, "")
		if err != nil {
			fail(fmt.Errorf("the channel could not be read: %s", describeREST(err)), "")
			return
		}
		r.mu.Lock()
		if len(newest) > 0 {
			c.row.Cutoff = newest[0].ID
		}
		r.mu.Unlock()
		if err := r.saveChannel(c); err != nil {
			fail(err, "")
			return
		}
		r.activity("info", "command", who+" reset the conversation", in.guildID, in.channelID, p.GetName(), "")
		r.respond(in, p, "Forgotten. The conversation starts fresh from here.", nil)
	case "persona":
		if personaName == "" {
			var lines []string
			for _, cand := range r.spec.GetPersonas() {
				mark := ""
				if cand.GetId() == p.GetId() {
					mark = " (speaking here)"
				}
				lines = append(lines, "• "+cand.GetName()+mark)
			}
			r.respond(in, p, "Personas:\n"+strings.Join(lines, "\n"), nil)
			return
		}
		chosen, ok := r.personaNamed(personaName)
		if !ok {
			fail(fmt.Errorf("no persona named %s", personaName), "")
			return
		}
		r.mu.Lock()
		c.row.PersonaID = chosen.GetId()
		r.mu.Unlock()
		if err := r.saveChannel(c); err != nil {
			fail(err, "")
			return
		}
		r.activity("info", "command", who+" chose persona "+chosen.GetName(), in.guildID, in.channelID, chosen.GetName(), "")
		r.respond(in, chosen, chosen.GetName()+" speaks here now.", nil)
	case "ask":
		if prompt == "" {
			fail(fmt.Errorf("ask needs a prompt"), "")
			return
		}
		model, err := r.route(p, "chat")
		if err != nil {
			fail(err, "")
			return
		}
		chat := r.freshChat(p, in.sess, in.channelID, r.namedText(in.user, prompt))
		if attachment != nil && p.GetVision() {
			parts, notes, err := r.attachmentParts(ctx, &discordgo.Message{Attachments: []*discordgo.MessageAttachment{attachment}})
			if err != nil {
				fail(err, "")
				return
			}
			last := &chat.Messages[len(chat.Messages)-1]
			if len(notes) > 0 {
				last.Parts[0].Text = strings.TrimSpace(last.Parts[0].Text + " " + strings.Join(notes, " "))
			}
			last.Parts = append(parts, last.Parts...)
		}
		stop := r.typing(ctx, in.sess, in.channelID)
		ans, err := r.chat(ctx, p, model, chat, nil)
		stop()
		trace := ""
		if ans != nil {
			trace = ans.trace
		}
		if err != nil {
			fail(err, trace)
			return
		}
		text := r.style(p, ans.text)
		if strings.TrimSpace(text) == "" {
			fail(fmt.Errorf("the model answered with nothing"), trace)
			return
		}
		if err := r.respond(in, p, text, nil); err != nil {
			fail(err, trace)
			return
		}
		r.count(func(s *v1.BotStatus) { s.Replies++ })
		r.activity("info", "command", "answered "+who, in.guildID, in.channelID, p.GetName(), trace)
	case "imagine":
		if prompt == "" {
			fail(fmt.Errorf("imagine needs a prompt"), "")
			return
		}
		model, err := r.route(p, "image")
		if err != nil {
			fail(err, "")
			return
		}
		init := ""
		if attachment != nil {
			if !isImage(attachment) {
				fail(fmt.Errorf("%s is not an image", attachment.Filename), "")
				return
			}
			data, err := r.download(ctx, attachment.URL, attachment.Size)
			if err != nil {
				fail(err, "")
				return
			}
			init = base64.StdEncoding.EncodeToString(data)
		}
		stop := r.typing(ctx, in.sess, in.channelID)
		files, trace, err := r.images(ctx, p, model, prompt, init, count)
		stop()
		if err != nil {
			fail(err, trace)
			return
		}
		if err := r.respond(in, p, r.caption(who, prompt), files); err != nil {
			fail(err, trace)
			return
		}
		r.count(func(s *v1.BotStatus) { s.Images += uint64(len(files)) })
		r.activity("info", "command", fmt.Sprintf("made %d images for %s", len(files), who), in.guildID, in.channelID, p.GetName(), trace)
	case "video":
		if prompt == "" {
			fail(fmt.Errorf("video needs a prompt"), "")
			return
		}
		model, err := r.route(p, "video")
		if err != nil {
			fail(err, "")
			return
		}
		init := ""
		var frames []string
		if attachment != nil {
			data, err := r.download(ctx, attachment.URL, attachment.Size)
			if err != nil {
				fail(err, "")
				return
			}
			switch {
			case isImage(attachment):
				init = base64.StdEncoding.EncodeToString(data)
			case isVideo(attachment):
				want := int(r.spec.GetMedia().GetVideoFrames())
				if want == 0 {
					fail(fmt.Errorf("video input requires media.video_frames to sample reference frames"), "")
					return
				}
				frames, err = r.frames(ctx, data, attachment.Filename, want)
				if err != nil {
					fail(err, "")
					return
				}
			default:
				fail(fmt.Errorf("%s is neither an image nor a video", attachment.Filename), "")
				return
			}
		}
		stop := r.typing(ctx, in.sess, in.channelID)
		f, trace, err := r.video(ctx, p, model, prompt, init, frames)
		stop()
		if err != nil {
			fail(err, trace)
			return
		}
		if err := r.fits(f); err != nil {
			fail(err, trace)
			return
		}
		if err := r.respond(in, p, r.caption(who, prompt), []file{*f}); err != nil {
			fail(err, trace)
			return
		}
		r.count(func(s *v1.BotStatus) { s.Videos++ })
		r.activity("info", "command", "made a video for "+who, in.guildID, in.channelID, p.GetName(), trace)
	}
}

// Prefixes the caller's name when configured.
func (r *runner) namedText(u *discordgo.User, text string) string {
	if !r.spec.GetMemory().GetIncludeNames() || u == nil {
		return text
	}
	return u.DisplayName() + ": " + text
}

// Generation caption.
func (r *runner) caption(who, prompt string) string {
	if len(prompt) > 200 {
		prompt = prompt[:200] + "…"
	}
	return fmt.Sprintf("**%s** · %s", who, prompt)
}

func (r *runner) helpText() string {
	c := r.spec.GetCommands()
	e := r.spec.GetEngagement()
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** routes chat, images, and video through nebu.\n", r.name)
	lines := map[string]string{
		"ask":     "ask the model something, an image attached if it can see",
		"imagine": "make images from a prompt, an image attached to start from",
		"video":   "make a video from a prompt, an image or a video attached to start from",
		"persona": "see or choose who speaks in this channel",
		"models":  "list the models the bot can reach",
		"reset":   "forget this channel's conversation so far",
		"help":    "this",
	}
	for _, role := range commandRoles {
		if contains(c.GetDisabled(), role) {
			continue
		}
		fmt.Fprintf(&b, "• `/%s` %s\n", c.GetNames()[role], lines[role])
		if e.GetPrefix() != "" {
			fmt.Fprintf(&b, "  also `%s%s`\n", e.GetPrefix(), c.GetNames()[role])
		}
	}
	if e.GetRequireMention() {
		b.WriteString("Mention the bot, reply to it, or say a persona's name to talk to it.")
	} else {
		b.WriteString("The bot answers in the channels it is allowed in.")
	}
	return b.String()
}

func (r *runner) modelsText() string {
	var lines []string
	for _, rt := range r.m.Gateway.Table().Ready() {
		kind := "chat"
		if rt.GetApi() == v1.ApiFlavor_API_FLAVOR_SDCPP {
			var modes []string
			for _, m := range rt.GetModes() {
				switch m {
				case "img_gen":
					modes = append(modes, "images")
				case "vid_gen":
					modes = append(modes, "video")
				}
			}
			kind = strings.Join(modes, ", ")
		}
		lines = append(lines, fmt.Sprintf("• `%s` %s", rt.GetName(), kind))
	}
	if len(lines) == 0 {
		return "No model is running. Start one with `nebu run` or from the nebu UI."
	}
	sort.Strings(lines)
	return "Models ready now:\n" + strings.Join(lines, "\n")
}
