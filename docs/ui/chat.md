# Chat

Talks to a ready route through the gateway, saves conversations per account.

Route: `/chat`, `/chat/<id>`

## Default view

```
┌──────────────┬────────────────────────────────────────┬──────────────┐
│ ⌕        [+] │ llama-70b ▾                        ⚙   │ Settings     │
│              │                                        │              │
│              │  You                                   │ Dialect      │
│ Today        │  Summarise the attached log            │ [openai  ▾]  │
│  Parser      │  [img]                                 │              │
│  Garden      │                                        │ System       │
│ Yesterday    │  llama-70b                      (i)    │ [          ] │
│  Fox image   │  Three restarts, each after ...        │              │
│              │  ▸ web_search  310ms  ✓                │ Sampling  ▾  │
│              │                                        │ Tools     ▾  │
│              │                                        │ Names     ▾  │
│              ├────────────────────────────────────────┤              │
│              │ [📎] Message                   [Send]  │              │
└──────────────┴────────────────────────────────────────┴──────────────┘
```

**Conversations.** 280px. Search (daemon query) and a New button on one row, then rows grouped by day: title (24ch). Overflow menu: Rename, Delete. The open conversation's title is the page title.

**Messages.** Model picker: a Picker over ready routes, text routes first, each option with context and quantization in its tooltip, disabled while none is ready. Turns: name, text as markdown, images as thumbnails. A gauge mark on each assistant turn opens a popover: TTFT, tokens per second, prompt and completion tokens, stop, trace link. A tool call is one line, name, duration, mark, expanding to arguments and result. A failed turn shows one error line and Retry. Streaming appends. Stop replaces Send while streaming.

**Composer.** Textarea to eight lines, enter sends. Attach appears only when the route takes images. Image routes replace the composer with Prompt, Size, Steps, Count. Video routes add Seconds and FPS and show a progress turn until the file arrives.

**Settings.** 320px, collapsible. Sections collapsed except Dialect and System.

## Fields

| Field | Component | Source |
| --- | --- | --- |
| dialect | Segmented | openai, anthropic, ollama |
| system | Textarea | |
| stream | Switch | |
| temperature | Slider | 0 to 2, step 0.05, the dialect's default while unset |
| top_p | Slider | 0 to 1, step 0.01, the dialect's default while unset |
| top_k | Number | 0 and up, the dialect's default while unset |
| max_tokens | Slider | 1 to the route's context length, the whole context while unset |
| stop | Chips | |
| seed | Number | a shuffle mark while unset |
| tool_servers | Checklist | `ListToolServers` mesh wide, failed ones disabled with the error in a tooltip |
| mutating_tools | Switch | |
| approve_tools | Switch | calls then wait for Run in the chat |
| tools | Code json | client tools |
| user_name, assistant_name | Text | 32 |
| image size | Select | 512x512, 768x768, 1024x1024, 1024x768, 768x1024 |
| image steps | Slider | 1 to 100 |
| image count | Slider | 1 to 4 |
| video seconds | Slider | 1 to 30 |
| video fps | Select | 8, 12, 16, 24, 30 |

**Tool handling.** Approve off: the request names the servers and the gateway runs calls, shown as they stream. Approve on: the UI sends server tools as client tools, each call shows Run and Skip, runs through `CallTool`, appends a tool turn, continues. Custom tools show a result box.

## Widths

Ultrawide: three columns, 280 / fill / 320. Standard: conversations collapse to an icon strip, settings drawer. Mobile: messages alone, conversations and settings as sheets from the header.

## Data

`ListConversations`, `GetConversation`, `PutConversation` after each turn, `RenameConversation`, `DeleteConversation`, `PutChatFile`, `GetChatFile`, `DeleteChatFiles`, `ListToolServers`, `CallTool`. Gateway `/v1/chat/completions`, `/v1/messages`, `/api/chat`, `/v1/images/generations`, `/v1/videos`. Stream: ROUTE, TRACE.

## Questions

- Pin the model to the conversation, or follow the current pick on reopen?
- Image edits from an attached image in the first version?
