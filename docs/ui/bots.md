# Bots

Discord bots answering through the gateway.

Route: `/bots`, `/bots/<id>/<tab>`

## Default view

```
 [+ New bot]

 ◉  Nebby     ●  ready      12 guilds     [Stop]   [Open]
 ◉  Muse      ○  stopped                  [Start]  [Open]
```

Rows: avatar, name (24ch), state dot, guild count, Start or Stop, Open. Tooltip: username, shards and latency, replies, images, videos, errors.

**New bot.** Name Text 64, token Secret, Probe shows username, avatar, recommended shards and the invite link, enabled Switch, Create.

## Bot page

Tabs: Status, Personas, Automations, Behaviour, Activity, Send. Settings tabs edit a copy of the spec, a save bar appears when dirty, Save says the bot restarts when running. Danger under Status: change token, Delete by name.

**Status.** Rows: username, invite link with copy, started, guilds, counters. Shards rows: id, connected dot, guilds, latency. Tooltip: connected at, error.

**Personas.** Rows: avatar, name, model (24ch), default badge, drag handle, menu: Edit, Duplicate, Delete. Editor sections: Identity, Models, Sampling, Behaviour, Channels, Media, Tools.

**Automations.** Rows: enabled Switch, name, trigger word, action word, menu: Edit, Delete. Editor sections: Trigger, Action, Scope.

**Behaviour.** Sections: Engagement, Commands, Memory, Media, Presence, Shards.

**Activity.** Rows: time, level dot, kind, message (64ch). Tooltip: persona, guild, channel. Trace link icon when set. Filters: level Select, kind Select, from the daemon.

**Send.** Guild Select, channel Select from `ListBotGuilds`, persona Select, kind Segmented, content Textarea, Send. Result: message id and trace link in a toast.

## Fields

| Field | Component | Source |
| --- | --- | --- |
| name | Text | 64 |
| token | Secret | |
| enabled | Switch | |
| shard_count | Number | 0 recommended |
| shard_ids | Chips | numbers |
| presence status | Segmented | online, idle, dnd, invisible |
| activity_type | Select | playing, listening, watching, competing, custom |
| activities | Chips | |
| rotate_seconds | Duration | 0 is five minutes |
| avatar_url | Text | url, preview |
| webhook | Switch | |
| system_prompt | Textarea | |
| model, image_model, video_model | Picker | `ListRoutes` by modality, empty is the sole ready one |
| temperature | Slider | 0 to 2 |
| top_p | Slider | 0 to 1 |
| top_k | Number | |
| max_tokens | Number | |
| stop | Chips | |
| seed | Number | |
| humanize enabled | Switch | shows the rest |
| chars_per_second | Slider | 1 to 100 |
| delay_min_ms, delay_max_ms, max_typing_ms | Duration | |
| split_messages | Switch | |
| max_chunk_chars | Slider | 0 to 2000 |
| casual, quote_reply | Switch | |
| reaction_chance, ambient_reply_chance, ignore_chance | Percent | 0 to 100 as a fraction |
| reactions | Chips | emoji |
| active_hours | two time inputs | HH:MM |
| timezone | Select | IANA list |
| wake_words | Chips | |
| channel_ids, guild_ids, denied_channel_ids | Checklist | `ListBotGuilds` tree |
| user_ids, denied_user_ids | Chips | |
| vision, stream, answer_bots, direct_messages, require_mention, member_events | Switch | |
| image_style, negative_prompt | Textarea | |
| tool_servers | Checklist | `ListToolServers` |
| mutating_tools | Switch | |
| max_bot_chain, max_concurrent | Number | 0 default shown as placeholder |
| prefix | Text | 1 |
| cooldown_ms, user_cooldown_ms | Duration | |
| commands enabled | Switch | |
| command names | KeyValues | roles fixed, names editable |
| disabled commands | MultiSelect | ask, imagine, video, persona, models, reset, help |
| memory messages, max_chars | Number | |
| window_minutes | Duration | |
| include_names | Switch | |
| video_frames | Slider | 0 to 32 |
| max_upload_bytes | Bytes | |
| image_size, video_size | Select | size list |
| image_steps | Slider | 1 to 100 |
| video_seconds | Slider | 1 to 30 |
| video_fps | Select | 8, 12, 16, 24, 30 |
| image_count | Slider | 1 to 4 |
| trigger kind | Select | seven kinds |
| cron | Cron | |
| every_seconds | Duration | |
| pattern | Text | regex or emoji by kind |
| command | Text | 32 |
| action kind | Select | six kinds |
| template | Textarea | variables listed in `(i)` |
| emoji | Text | 8 |
| reply | Switch | |
| action model | Picker | `ListRoutes` |
| persona | Select | the bot's personas |
| chance | Percent | 0 always |
| cooldown_ms | Duration | |

## Widths

Ultrawide: the bot page as a Split, list of personas or automations left, editor right. Standard: editor as a drawer. Mobile: tabs scroll as a Select, rows drop the model and guild count into the expand.

## Data

`ListBots`, `GetBot`, `CreateBot`, `UpdateBot`, `DeleteBot`, `StartBot`, `StopBot`, `ListBotActivity` with filters and cursor, `ListBotGuilds`, `ProbeBotToken`, `SendBotMessage`, `ListRoutes`, `ListToolServers`. Stream: BOT, BOT_ACTIVITY.
