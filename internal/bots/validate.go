package bots

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	// Discord's own cap on a message
	discordMessageMax     = 2000
	defaultCharsPerSecond = 45.0
	defaultMaxTypingMs    = 12000
	defaultHistory        = 20
	maxHistory            = 100
	defaultHistoryChars   = 8000
	defaultUploadBytes    = 10_000_000
	defaultImageSize      = "1024x1024"
	defaultBotChain       = 3
	defaultConcurrent     = 4
	defaultRotateSeconds  = 300
	maxImageCount         = 4
	maxVideoFrames        = 32
)

// The roles slash commands come in, each with its default name
var commandRoles = []string{"ask", "imagine", "video", "persona", "models", "reset", "help"}

var (
	commandName = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	activeHours = regexp.MustCompile(`^(\d{1,2}):(\d{2})-(\d{1,2}):(\d{2})$`)
	sizePattern = regexp.MustCompile(`^(\d+)x(\d+)$`)
)

// Fills defaults, assigns ids, and refuses settings that cannot work, so a saved bot always runs as written
func normalize(botName string, s *v1.BotSpec) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrBot}, args...)...)
	}
	if s.ShardCount == 0 && len(s.ShardIds) > 0 {
		return bad("shard ids need a shard count, so every host opening part of the bot agrees on the total")
	}
	for _, id := range s.ShardIds {
		if id >= s.ShardCount {
			return bad("shard id %d is past the shard count %d", id, s.ShardCount)
		}
	}
	if s.Presence == nil {
		s.Presence = &v1.Presence{}
	}
	if s.Presence.Status == "" {
		s.Presence.Status = "online"
	}
	switch s.Presence.Status {
	case "online", "idle", "dnd", "invisible":
	default:
		return bad("presence status %q: one of online, idle, dnd, invisible", s.Presence.Status)
	}
	if s.Presence.ActivityType == "" {
		s.Presence.ActivityType = "playing"
	}
	switch s.Presence.ActivityType {
	case "playing", "listening", "watching", "competing", "custom":
	default:
		return bad("activity type %q: one of playing, listening, watching, competing, custom", s.Presence.ActivityType)
	}
	if s.Presence.RotateSeconds == 0 {
		s.Presence.RotateSeconds = defaultRotateSeconds
	}
	s.Presence.Activities = trimAll(s.Presence.Activities)
	if s.Engagement == nil {
		s.Engagement = &v1.Engagement{DirectMessages: true, RequireMention: true}
	}
	e := s.Engagement
	if e.MaxBotChain == 0 {
		e.MaxBotChain = defaultBotChain
	}
	if e.MaxConcurrent == 0 {
		e.MaxConcurrent = defaultConcurrent
	}
	e.Prefix = strings.TrimSpace(e.Prefix)
	if strings.ContainsAny(e.Prefix, " \t\n") {
		return bad("the command prefix cannot contain spaces")
	}
	e.ChannelIds, e.DeniedChannelIds, e.GuildIds, e.UserIds, e.DeniedUserIds = trimAll(e.ChannelIds), trimAll(e.DeniedChannelIds), trimAll(e.GuildIds), trimAll(e.UserIds), trimAll(e.DeniedUserIds)
	if s.Commands == nil {
		s.Commands = &v1.Commands{Enabled: true}
	}
	if s.Commands.Names == nil {
		s.Commands.Names = map[string]string{}
	}
	for role := range s.Commands.Names {
		if !knownRole(role) {
			return bad("command role %q: one of %s", role, strings.Join(commandRoles, ", "))
		}
	}
	seen := map[string]string{}
	for _, role := range commandRoles {
		name := strings.TrimSpace(s.Commands.Names[role])
		if name == "" {
			name = role
		}
		if !commandName.MatchString(name) {
			return bad("command name %q for %s: lowercase letters, digits, - and _, at most 32", name, role)
		}
		if other, dup := seen[name]; dup {
			return bad("commands %s and %s share the name %s", other, role, name)
		}
		seen[name] = role
		s.Commands.Names[role] = name
	}
	for _, role := range s.Commands.Disabled {
		if !knownRole(role) {
			return bad("disabled command %q: one of %s", role, strings.Join(commandRoles, ", "))
		}
	}
	s.Commands.GuildIds = trimAll(s.Commands.GuildIds)
	if s.Memory == nil {
		s.Memory = &v1.Memory{IncludeNames: true}
	}
	if s.Memory.Messages == 0 {
		s.Memory.Messages = defaultHistory
	}
	if s.Memory.Messages > maxHistory {
		return bad("memory reads at most %d messages back", maxHistory)
	}
	if s.Memory.MaxChars == 0 {
		s.Memory.MaxChars = defaultHistoryChars
	}
	if s.Media == nil {
		s.Media = &v1.Media{}
	}
	if s.Media.MaxUploadBytes == 0 {
		s.Media.MaxUploadBytes = defaultUploadBytes
	}
	if s.Media.ImageSize == "" {
		s.Media.ImageSize = defaultImageSize
	}
	if _, _, err := parseSize(s.Media.ImageSize); err != nil {
		return bad("image size: %v", err)
	}
	if s.Media.VideoSize != "" {
		if _, _, err := parseSize(s.Media.VideoSize); err != nil {
			return bad("video size: %v", err)
		}
	}
	if s.Media.ImageCount == 0 {
		s.Media.ImageCount = 1
	}
	if s.Media.ImageCount > maxImageCount {
		return bad("at most %d images per request", maxImageCount)
	}
	if s.Media.VideoFrames > maxVideoFrames {
		return bad("at most %d frames are sampled from a video", maxVideoFrames)
	}
	if len(s.Personas) == 0 {
		s.Personas = []*v1.Persona{{Name: botName, Vision: true}}
	}
	names := map[string]bool{}
	ids := map[string]bool{}
	for i, p := range s.Personas {
		p.Name = strings.Join(strings.Fields(p.Name), " ")
		if p.Name == "" {
			return bad("persona %d needs a name", i+1)
		}
		if len(p.Name) > 80 {
			return bad("persona name %q is longer than 80 characters", p.Name)
		}
		key := strings.ToLower(p.Name)
		if names[key] {
			return bad("two personas are named %s", p.Name)
		}
		names[key] = true
		if p.Id == "" {
			p.Id = db.NewID()
		}
		if ids[p.Id] {
			return bad("two personas share the id %s", p.Id)
		}
		ids[p.Id] = true
		p.WakeWords = trimAll(p.WakeWords)
		p.ChannelIds = trimAll(p.ChannelIds)
		p.Model, p.ImageModel, p.VideoModel = strings.TrimSpace(p.Model), strings.TrimSpace(p.ImageModel), strings.TrimSpace(p.VideoModel)
		if p.Sampling == nil {
			p.Sampling = &v1.Sampling{}
		}
		if t := p.Sampling.Temperature; t != nil && (*t < 0 || *t > 2) {
			return bad("persona %s: temperature %g is not between 0 and 2", p.Name, *t)
		}
		if tp := p.Sampling.TopP; tp != nil && (*tp <= 0 || *tp > 1) {
			return bad("persona %s: top_p %g is not between 0 and 1", p.Name, *tp)
		}
		if p.Humanize == nil {
			p.Humanize = &v1.Humanize{}
		}
		if err := checkHumanize(p.Humanize); err != nil {
			return bad("persona %s: %v", p.Name, err)
		}
	}
	autoIDs := map[string]bool{}
	for i, a := range s.Automations {
		a.Name = strings.TrimSpace(a.Name)
		if a.Name == "" {
			a.Name = fmt.Sprintf("automation %d", i+1)
		}
		if a.Id == "" {
			a.Id = db.NewID()
		}
		if autoIDs[a.Id] {
			return bad("two automations share the id %s", a.Id)
		}
		autoIDs[a.Id] = true
		if a.PersonaId != "" && !ids[a.PersonaId] {
			return bad("automation %s names a persona that does not exist", a.Name)
		}
		a.ChannelIds, a.GuildIds = trimAll(a.ChannelIds), trimAll(a.GuildIds)
		if a.Chance < 0 || a.Chance > 1 {
			return bad("automation %s: chance %g is not between 0 and 1", a.Name, a.Chance)
		}
		if err := checkAutomation(a, e); err != nil {
			return bad("automation %s: %v", a.Name, err)
		}
	}
	return nil
}

func checkHumanize(h *v1.Humanize) error {
	if h.CharsPerSecond == 0 {
		h.CharsPerSecond = defaultCharsPerSecond
	}
	if h.CharsPerSecond < 0 {
		return fmt.Errorf("chars per second cannot be negative")
	}
	if h.DelayMaxMs < h.DelayMinMs {
		return fmt.Errorf("the longest delay %d ms is shorter than the shortest %d ms", h.DelayMaxMs, h.DelayMinMs)
	}
	if h.MaxTypingMs == 0 {
		h.MaxTypingMs = defaultMaxTypingMs
	}
	if h.MaxChunkChars == 0 || h.MaxChunkChars > discordMessageMax {
		h.MaxChunkChars = discordMessageMax
	}
	for _, c := range []struct {
		name string
		v    float64
	}{{"reaction chance", h.ReactionChance}, {"ambient reply chance", h.AmbientReplyChance}, {"ignore chance", h.IgnoreChance}} {
		if c.v < 0 || c.v > 1 {
			return fmt.Errorf("%s %g is not between 0 and 1", c.name, c.v)
		}
	}
	if h.ReactionChance > 0 && len(trimAll(h.Reactions)) == 0 {
		return fmt.Errorf("a reaction chance needs emojis to react with")
	}
	h.Reactions = trimAll(h.Reactions)
	h.ActiveHours = strings.TrimSpace(h.ActiveHours)
	if h.ActiveHours != "" {
		if _, _, err := parseHours(h.ActiveHours); err != nil {
			return err
		}
	}
	h.Timezone = strings.TrimSpace(h.Timezone)
	if h.Timezone != "" {
		if _, err := time.LoadLocation(h.Timezone); err != nil {
			return fmt.Errorf("timezone %q is not an IANA zone", h.Timezone)
		}
	}
	return nil
}

func checkAutomation(a *v1.Automation, e *v1.Engagement) error {
	t, act := a.GetTrigger(), a.GetAction()
	if t == nil || t.Kind == v1.TriggerKind_TRIGGER_KIND_UNSPECIFIED {
		return fmt.Errorf("a trigger is required")
	}
	if act == nil || act.Kind == v1.ActionKind_ACTION_KIND_UNSPECIFIED {
		return fmt.Errorf("an action is required")
	}
	switch t.Kind {
	case v1.TriggerKind_TRIGGER_KIND_SCHEDULE:
		t.Cron = strings.TrimSpace(t.Cron)
		if t.Cron == "" && t.EverySeconds == 0 {
			return fmt.Errorf("a schedule needs a cron line or an interval")
		}
		if t.Cron != "" {
			if _, err := parseCron(t.Cron); err != nil {
				return err
			}
		}
		if len(a.ChannelIds) == 0 && act.Kind != v1.ActionKind_ACTION_KIND_PRESENCE {
			return fmt.Errorf("a schedule needs the channels it posts to")
		}
	case v1.TriggerKind_TRIGGER_KIND_KEYWORD:
		if strings.TrimSpace(t.Pattern) == "" {
			return fmt.Errorf("a keyword trigger needs a pattern")
		}
		if _, err := regexp.Compile("(?i)" + t.Pattern); err != nil {
			return fmt.Errorf("pattern: %v", err)
		}
	case v1.TriggerKind_TRIGGER_KIND_REACTION:
		if strings.TrimSpace(t.Pattern) == "" {
			return fmt.Errorf("a reaction trigger needs the emoji to watch for")
		}
	case v1.TriggerKind_TRIGGER_KIND_COMMAND:
		t.Command = strings.ToLower(strings.TrimSpace(t.Command))
		if t.Command == "" {
			return fmt.Errorf("a command trigger needs the word after the prefix")
		}
		if e.GetPrefix() == "" {
			return fmt.Errorf("a command trigger needs a command prefix under engagement")
		}
	case v1.TriggerKind_TRIGGER_KIND_MEMBER_JOIN:
		if !e.GetMemberEvents() {
			return fmt.Errorf("a member join trigger needs member events on under engagement, and the Server Members intent in the developer portal")
		}
	}
	switch act.Kind {
	case v1.ActionKind_ACTION_KIND_REACT:
		if strings.TrimSpace(act.Emoji) == "" {
			return fmt.Errorf("a react action needs an emoji")
		}
		if t.Kind == v1.TriggerKind_TRIGGER_KIND_SCHEDULE || t.Kind == v1.TriggerKind_TRIGGER_KIND_MEMBER_JOIN {
			return fmt.Errorf("a react action needs a message to react to")
		}
	case v1.ActionKind_ACTION_KIND_CHAT, v1.ActionKind_ACTION_KIND_IMAGE, v1.ActionKind_ACTION_KIND_VIDEO, v1.ActionKind_ACTION_KIND_TEXT, v1.ActionKind_ACTION_KIND_PRESENCE:
		if strings.TrimSpace(act.Template) == "" {
			return fmt.Errorf("the action needs a template")
		}
		if _, err := template.New("").Parse(act.Template); err != nil {
			return fmt.Errorf("template: %v", err)
		}
	}
	return nil
}

func knownRole(role string) bool {
	for _, r := range commandRoles {
		if r == role {
			return true
		}
	}
	return false
}

func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Reads WIDTHxHEIGHT, both multiples of sixteen as the diffusion runtime wants them
func parseSize(s string) (int, int, error) {
	m := sizePattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if m == nil {
		return 0, 0, fmt.Errorf("%q is not WIDTHxHEIGHT", s)
	}
	w, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	if w <= 0 || h <= 0 || w%16 != 0 || h%16 != 0 {
		return 0, 0, fmt.Errorf("%dx%d: width and height must be multiples of 16", w, h)
	}
	return w, h, nil
}

// Reads HH:MM-HH:MM as minutes of the day
func parseHours(s string) (int, int, error) {
	m := activeHours.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, fmt.Errorf("active hours %q are not HH:MM-HH:MM", s)
	}
	h1, _ := strconv.Atoi(m[1])
	m1, _ := strconv.Atoi(m[2])
	h2, _ := strconv.Atoi(m[3])
	m2, _ := strconv.Atoi(m[4])
	if h1 > 23 || h2 > 23 || m1 > 59 || m2 > 59 {
		return 0, 0, fmt.Errorf("active hours %q: hours to 23, minutes to 59", s)
	}
	return h1*60 + m1, h2*60 + m2, nil
}
