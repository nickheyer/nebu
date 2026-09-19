package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"sigs.k8s.io/yaml"
)

func botCommands() command {
	return command{name: "bots", summary: "Discord bots that route chat, images, and video through the gateway", run: runBotsList, sub: []command{
		{name: "list", summary: "list bots", run: runBotsList},
		{name: "show", summary: "show a bot, its shards, and its counters", run: runBotsShow},
		{name: "create", summary: "create a bot from a token and a settings file", run: runBotsCreate},
		{name: "update", summary: "change a bot's settings, token, or name", run: runBotsUpdate},
		{name: "export", summary: "print a bot's settings as YAML, to edit and pass back to update", run: runBotsExport},
		{name: "remove", summary: "delete a bot", run: runBotsRemove},
		{name: "start", summary: "connect a bot", run: runBotsStart},
		{name: "stop", summary: "disconnect a bot", run: runBotsStop},
		{name: "activity", summary: "show or follow what a bot did", run: runBotsActivity},
		{name: "guilds", summary: "list the guilds and channels a running bot sees", run: runBotsGuilds},
		{name: "probe", summary: "check a token with Discord", run: runBotsProbe},
		{name: "say", summary: "post to a channel through a running bot", run: runBotsSay},
	}}
}

func runBotsList(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("bots list"), args, 0, 0, "bots list"); err != nil {
		return err
	}
	resp, err := e.cl.bots.ListBots(ctx, connect.NewRequest(&v1.ListBotsRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { botsTable(w, resp.Msg.GetBots()) })
}

func botsTable(w io.Writer, list []*v1.Bot) {
	var rows [][]string
	for _, b := range list {
		st := b.GetStatus()
		up := 0
		for _, s := range st.GetShards() {
			if s.GetConnected() {
				up++
			}
		}
		rows = append(rows, []string{b.GetId(), b.GetName(), loud(b.GetState()), onOff(b.GetEnabled()), fmt.Sprintf("%d/%d", up, len(st.GetShards())), fmt.Sprint(st.GetGuilds()), fmt.Sprint(len(b.GetSpec().GetPersonas())), fmt.Sprintf("%d/%d/%d", st.GetReplies(), st.GetImages(), st.GetVideos()), fmt.Sprint(st.GetErrors()), b.GetError()})
	}
	table(w, []string{"ID", "NAME", "STATE", "ENABLED", "SHARDS", "GUILDS", "PERSONAS", "REPLIES/IMAGES/VIDEOS", "ERRORS", "ERROR"}, rows)
}

func onOff(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func runBotsShow(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("bots show"), args, 1, 1, "bots show <name|id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.bots.GetBot(ctx, connect.NewRequest(&v1.GetBotRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		b := resp.Msg.GetBot()
		st := b.GetStatus()
		fmt.Fprintf(w, "%s %s %s\n", b.GetId(), b.GetName(), loud(b.GetState()))
		rows := [][]string{
			{"enabled", onOff(b.GetEnabled())},
			{"token", onOff(b.GetTokenSet())},
			{"user", st.GetUsername() + " " + st.GetUserId()},
			{"application", st.GetApplicationId()},
			{"invite", st.GetInviteUrl()},
			{"guilds", fmt.Sprint(st.GetGuilds())},
			{"recommended shards", fmt.Sprint(st.GetRecommendedShards())},
			{"replies", fmt.Sprint(st.GetReplies())},
			{"images", fmt.Sprint(st.GetImages())},
			{"videos", fmt.Sprint(st.GetVideos())},
			{"errors", fmt.Sprint(st.GetErrors())},
			{"started", when(st.GetStartedAt(), time.RFC3339)},
			{"created", when(b.GetCreatedAt(), time.RFC3339)},
			{"updated", when(b.GetUpdatedAt(), time.RFC3339)},
		}
		if b.GetError() != "" {
			rows = append(rows, []string{"error", b.GetError()})
		}
		table(w, nil, rows)
		if len(st.GetShards()) > 0 {
			section(w, "shards")
			var srows [][]string
			for _, s := range st.GetShards() {
				state := "down"
				if s.GetConnected() {
					state = "up"
				}
				srows = append(srows, []string{fmt.Sprint(s.GetId()), state, fmt.Sprint(s.GetGuilds()), fmt.Sprintf("%d ms", s.GetLatencyMs()), when(s.GetConnectedAt(), time.RFC3339), s.GetError()})
			}
			table(w, []string{"SHARD", "STATE", "GUILDS", "LATENCY", "SINCE", "ERROR"}, srows)
		}
		section(w, "personas")
		var prows [][]string
		for i, p := range b.GetSpec().GetPersonas() {
			mark := ""
			if i == 0 {
				mark = "default"
			}
			human := "no"
			if p.GetHumanize().GetEnabled() {
				human = "yes"
			}
			prows = append(prows, []string{p.GetId(), p.GetName(), mark, firstOr(p.GetModel(), "-"), firstOr(p.GetImageModel(), "-"), firstOr(p.GetVideoModel(), "-"), onOff(p.GetWebhook()), human, strings.Join(p.GetWakeWords(), ",")})
		}
		table(w, []string{"ID", "NAME", "", "MODEL", "IMAGES", "VIDEO", "WEBHOOK", "HUMAN", "WAKE WORDS"}, prows)
		if autos := b.GetSpec().GetAutomations(); len(autos) > 0 {
			section(w, "automations")
			var arows [][]string
			for _, a := range autos {
				arows = append(arows, []string{a.GetId(), a.GetName(), onOff(a.GetEnabled()), loud(a.GetTrigger().GetKind()), loud(a.GetAction().GetKind()), strings.Join(a.GetChannelIds(), ",")})
			}
			table(w, []string{"ID", "NAME", "ENABLED", "TRIGGER", "ACTION", "CHANNELS"}, arows)
		}
	})
}

func firstOr(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// Reads a settings file, YAML or JSON, into a spec
func readSpec(path string) (*v1.BotSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	js, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	spec := &v1.BotSpec{}
	if err := protojson.Unmarshal(js, spec); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return spec, nil
}

// Flags shared by create and update, applied over a spec
type botFlags struct {
	spec, model, imageModel, videoModel, system, persona, prefix string
	requireMention, dms, humanize                                string
}

func (f *botFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.spec, "spec", "", "a YAML or JSON settings file in the shape bots export prints")
	fs.StringVar(&f.model, "model", "", "the chat route of the default persona")
	fs.StringVar(&f.imageModel, "image-model", "", "the image route of the default persona")
	fs.StringVar(&f.videoModel, "video-model", "", "the video route of the default persona")
	fs.StringVar(&f.system, "system", "", "the system prompt of the default persona")
	fs.StringVar(&f.persona, "persona", "", "the name of the default persona")
	fs.StringVar(&f.prefix, "prefix", "", "the text command prefix, such as !")
	fs.StringVar(&f.requireMention, "require-mention", "", "true or false, whether guild messages need a mention, reply, or wake word")
	fs.StringVar(&f.dms, "dms", "", "true or false, whether direct messages are answered")
	fs.StringVar(&f.humanize, "humanize", "", "true or false, whether the default persona types and pauses like a person")
}

// Loads the spec file, then applies flag overrides.
func (f *botFlags) apply(fs *flag.FlagSet, spec *v1.BotSpec) (*v1.BotSpec, error) {
	if f.spec != "" {
		loaded, err := readSpec(f.spec)
		if err != nil {
			return nil, err
		}
		spec = loaded
	}
	if spec == nil {
		spec = &v1.BotSpec{}
	}
	if len(spec.Personas) == 0 {
		spec.Personas = []*v1.Persona{{Vision: true}}
	}
	if spec.Engagement == nil {
		spec.Engagement = &v1.Engagement{DirectMessages: true, RequireMention: true}
	}
	p := spec.Personas[0]
	var err error
	fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "model":
			p.Model = f.model
		case "image-model":
			p.ImageModel = f.imageModel
		case "video-model":
			p.VideoModel = f.videoModel
		case "system":
			p.SystemPrompt = f.system
		case "persona":
			p.Name = f.persona
		case "prefix":
			spec.Engagement.Prefix = f.prefix
		case "require-mention":
			spec.Engagement.RequireMention, err = parseBool(fl.Name, f.requireMention, err)
		case "dms":
			spec.Engagement.DirectMessages, err = parseBool(fl.Name, f.dms, err)
		case "humanize":
			if p.Humanize == nil {
				p.Humanize = &v1.Humanize{}
			}
			p.Humanize.Enabled, err = parseBool(fl.Name, f.humanize, err)
		}
	})
	return spec, err
}

func parseBool(name, v string, prior error) (bool, error) {
	if prior != nil {
		return false, prior
	}
	switch strings.ToLower(v) {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	}
	return false, fmt.Errorf("--%s takes true or false, got %q", name, v)
}

func runBotsCreate(ctx context.Context, e *env, args []string) error {
	fs := e.flags("bots create")
	var f botFlags
	f.bind(fs)
	token := fs.String("token", "", "Discord bot token or environment variable name")
	start := fs.Bool("start", false, "connect the bot as soon as it is created")
	positional, err := e.parse(fs, args, 1, 1, "bots create <name> --token T [--start] [--spec FILE] [--model M] [--image-model M] [--video-model M] [--system TEXT] [--persona NAME] [--prefix !] [--require-mention B] [--dms B] [--humanize B]")
	if err != nil {
		return err
	}
	spec, err := f.apply(fs, nil)
	if err != nil {
		return err
	}
	if spec.Personas[0].Name == "" {
		spec.Personas[0].Name = positional[0]
	}
	resp, err := e.cl.bots.CreateBot(ctx, connect.NewRequest(&v1.CreateBotRequest{Name: positional[0], Token: tokenValue(*token), Enabled: *start, Spec: spec}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { botsTable(w, []*v1.Bot{resp.Msg.GetBot()}) })
}

// Returns a literal token or its named environment variable.
func tokenValue(v string) string {
	if env := os.Getenv(v); v != "" && env != "" && !strings.Contains(v, ".") {
		return env
	}
	return v
}

func runBotsUpdate(ctx context.Context, e *env, args []string) error {
	fs := e.flags("bots update")
	var f botFlags
	f.bind(fs)
	token := fs.String("token", "", "a new token, the stored one kept when not given")
	rename := fs.String("name", "", "a new name")
	enabled := fs.String("enabled", "", "true or false, whether the daemon keeps the bot connected")
	positional, err := e.parse(fs, args, 1, 1, "bots update <name|id> [--name NEW] [--token T] [--enabled B] [--spec FILE] [flags as create]")
	if err != nil {
		return err
	}
	current, err := e.cl.bots.GetBot(ctx, connect.NewRequest(&v1.GetBotRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	cur := current.Msg.GetBot()
	spec, err := f.apply(fs, cur.GetSpec())
	if err != nil {
		return err
	}
	on := cur.GetEnabled()
	if *enabled != "" {
		if on, err = parseBool("enabled", *enabled, nil); err != nil {
			return err
		}
	}
	resp, err := e.cl.bots.UpdateBot(ctx, connect.NewRequest(&v1.UpdateBotRequest{Id: cur.GetId(), Name: *rename, Token: tokenValue(*token), Enabled: on, Spec: spec}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { botsTable(w, []*v1.Bot{resp.Msg.GetBot()}) })
}

func runBotsExport(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("bots export"), args, 1, 1, "bots export <name|id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.bots.GetBot(ctx, connect.NewRequest(&v1.GetBotRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	js, err := protojson.MarshalOptions{UseProtoNames: true, EmitDefaultValues: true}.Marshal(resp.Msg.GetBot().GetSpec())
	if err != nil {
		return err
	}
	if e.json {
		fmt.Fprintln(e.out, string(js))
		return nil
	}
	out, err := yaml.JSONToYAML(js)
	if err != nil {
		return err
	}
	fmt.Fprint(e.out, string(out))
	return nil
}

func runBotsRemove(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("bots remove"), args, 1, 1, "bots remove <name|id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.bots.DeleteBot(ctx, connect.NewRequest(&v1.DeleteBotRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "removed %s\n", resp.Msg.GetBot().GetName()) })
}

func runBotsStart(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("bots start"), args, 1, 1, "bots start <name|id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.bots.StartBot(ctx, connect.NewRequest(&v1.StartBotRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { botsTable(w, []*v1.Bot{resp.Msg.GetBot()}) })
}

func runBotsStop(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("bots stop"), args, 1, 1, "bots stop <name|id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.bots.StopBot(ctx, connect.NewRequest(&v1.StopBotRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { botsTable(w, []*v1.Bot{resp.Msg.GetBot()}) })
}

func runBotsActivity(ctx context.Context, e *env, args []string) error {
	fs := e.flags("bots activity")
	follow := fs.Bool("follow", false, "keep printing as the bot acts")
	limit := fs.Int("limit", 50, "lines to show, 0 for everything kept")
	positional, err := e.parse(fs, args, 1, 1, "bots activity <name|id> [--follow] [--limit N]")
	if err != nil {
		return err
	}
	bot, err := e.cl.bots.GetBot(ctx, connect.NewRequest(&v1.GetBotRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	id := bot.Msg.GetBot().GetId()
	resp, err := e.cl.bots.ListBotActivity(ctx, connect.NewRequest(&v1.ListBotActivityRequest{BotId: id, Limit: uint32(*limit)}))
	if err != nil {
		return err
	}
	if !*follow {
		return e.print(resp.Msg, func(w io.Writer) {
			list := resp.Msg.GetActivity()
			for i := len(list) - 1; i >= 0; i-- {
				printActivity(w, list[i])
			}
		})
	}
	list := resp.Msg.GetActivity()
	for i := len(list) - 1; i >= 0; i-- {
		printActivity(e.out, list[i])
	}
	stream, err := e.cl.events.WatchEvents(ctx, connect.NewRequest(&v1.WatchEventsRequest{Kinds: []v1.EventKind{v1.EventKind_EVENT_KIND_BOT_ACTIVITY}}))
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
		if a := stream.Msg().GetEvent().GetBotActivity(); a != nil && a.GetBotId() == id {
			printActivity(e.out, a)
		}
	}
	if err := stream.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func printActivity(w io.Writer, a *v1.BotActivity) {
	where := a.GetChannelId()
	if a.GetPersona() != "" {
		where = a.GetPersona() + "@" + where
	}
	fmt.Fprintf(w, "%s %-5s %-10s %-24s %s", when(a.GetAt(), time.RFC3339), a.GetLevel(), a.GetKind(), where, a.GetMessage())
	if a.GetTrace() != "" {
		fmt.Fprintf(w, " [trace %s]", a.GetTrace())
	}
	fmt.Fprintln(w)
}

func runBotsGuilds(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("bots guilds"), args, 1, 1, "bots guilds <name|id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.bots.ListBotGuilds(ctx, connect.NewRequest(&v1.ListBotGuildsRequest{BotId: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, g := range resp.Msg.GetGuilds() {
			for _, c := range g.GetChannels() {
				rows = append(rows, []string{g.GetId(), g.GetName(), c.GetId(), c.GetName(), c.GetKind()})
			}
			if len(g.GetChannels()) == 0 {
				rows = append(rows, []string{g.GetId(), g.GetName(), "", "", ""})
			}
		}
		table(w, []string{"GUILD", "NAME", "CHANNEL", "NAME", "KIND"}, rows)
	})
}

func runBotsProbe(ctx context.Context, e *env, args []string) error {
	fs := e.flags("bots probe")
	token := fs.String("token", "", "the token to check, or the name of an environment variable holding it")
	bot := fs.String("bot", "", "check the stored token of this bot instead")
	if _, err := e.parse(fs, args, 0, 0, "bots probe --token T | --bot NAME"); err != nil {
		return err
	}
	resp, err := e.cl.bots.ProbeBotToken(ctx, connect.NewRequest(&v1.ProbeBotTokenRequest{Token: tokenValue(*token), BotId: *bot}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		table(w, nil, [][]string{
			{"user", resp.Msg.GetUsername() + " " + resp.Msg.GetUserId()},
			{"application", resp.Msg.GetApplicationId()},
			{"recommended shards", fmt.Sprint(resp.Msg.GetRecommendedShards())},
			{"invite", resp.Msg.GetInviteUrl()},
		})
	})
}

func runBotsSay(ctx context.Context, e *env, args []string) error {
	fs := e.flags("bots say")
	kind := fs.String("kind", "text", "text, chat, image, or video: post the content, or generate from it")
	persona := fs.String("persona", "", "the persona that speaks, by id or name, the first when empty")
	positional, err := e.parse(fs, args, 3, -1, "bots say <name|id> <channel-id> <content...> [--kind text|chat|image|video] [--persona P]")
	if err != nil {
		return err
	}
	kinds := map[string]v1.ActionKind{"text": v1.ActionKind_ACTION_KIND_TEXT, "chat": v1.ActionKind_ACTION_KIND_CHAT, "image": v1.ActionKind_ACTION_KIND_IMAGE, "video": v1.ActionKind_ACTION_KIND_VIDEO}
	k, ok := kinds[strings.ToLower(*kind)]
	if !ok {
		return fmt.Errorf("--kind %q: one of text, chat, image, video", *kind)
	}
	personaID := *persona
	if personaID != "" {
		bot, err := e.cl.bots.GetBot(ctx, connect.NewRequest(&v1.GetBotRequest{Id: positional[0]}))
		if err != nil {
			return err
		}
		for _, p := range bot.Msg.GetBot().GetSpec().GetPersonas() {
			if strings.EqualFold(p.GetName(), personaID) {
				personaID = p.GetId()
			}
		}
	}
	resp, err := e.cl.bots.SendBotMessage(ctx, connect.NewRequest(&v1.SendBotMessageRequest{BotId: positional[0], ChannelId: positional[1], PersonaId: personaID, Kind: k, Content: strings.Join(positional[2:], " ")}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "posted %s", resp.Msg.GetMessageId())
		if resp.Msg.GetTrace() != "" {
			fmt.Fprintf(w, " trace %s", resp.Msg.GetTrace())
		}
		fmt.Fprintln(w)
	})
}
