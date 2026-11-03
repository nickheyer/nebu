# nebu web UI

Operator console for a nebu daemon.

Visual identity, palette and type: [style.md](style.md).

## Pages

| Page | Route | Roles |
| --- | --- | --- |
| [Login](login.md) | `/login` | everyone |
| [Overview](overview.md) | `/` | operator, admin |
| [Chat](chat.md) | `/chat`, `/chat/<id>` | everyone |
| [Models](models.md) | `/models` | operator, admin |
| [Discover](discover.md) | `/discover`, `/discover/<source>/<repo>` | operator, admin |
| [Runtimes](runtimes.md) | `/runtimes` | operator, admin |
| [Serve](serve.md) | `/serve` | operator, admin |
| [Gateway](gateway.md) | `/gateway/<tab>` | operator, admin |
| [Traffic](traffic.md) | `/traffic` | operator, admin |
| [Mesh](mesh.md) | `/mesh/<tab>` | operator reads, admin manages |
| [Bots](bots.md) | `/bots`, `/bots/<id>/<tab>` | operator, admin |
| [Tasks](tasks.md) | `/tasks` | operator, admin |
| [Host](host.md) | `/host` | operator, admin |
| [Settings](settings.md) | `/settings/<tab>` | admin, own tokens for everyone |

## Rules

1. A view shows only what the average case needs. Everything else sits one access layer away.
2. Nothing scrolls horizontally. Facts that do not fit a row move to an access layer, not to a longer row and not to a taller one.
3. Every field is typed to its data: a fixed set becomes a choice control, a bounded number becomes a range control, a daemon record becomes a searchable picker that queries the daemon. No free text where a choice exists.
4. Every string container has a width for its longest expected value and truncates with an ellipsis, the full value in a tooltip.
5. Space is balanced. Panels share the width in fixed ratios, tiles share rows equally, nothing floats alone.
6. Every page is specified for three widths.
7. No CSS grid, anywhere. Stacks, rows and splits only.
8. Standard patterns only: master and detail, tabs, stepper, toast, confirm dialog, empty state with one action. Labels, never sentences, in the UI.

## Widths

| Name | Width | Shell | Detail |
| --- | --- | --- | --- |
| Mobile | up to 720px | bottom bar, five tabs and More | full screen sheet |
| Standard | 721px to 1599px | left rail, labels on | drawer over the list, 480px |
| Ultrawide | 1600px and up | left rail | split, list 2/5 and detail 3/5, centred at 1920px max |

Content is centred with a 1440px maximum at standard width. Side margins stay equal at every width.

## Layout primitives

- **Stack.** Vertical, one gap token. The default for everything.
- **Row.** Horizontal, wraps. Each child declares a minimum and a maximum width. Used for tiles, chips, button groups and two fields side by side.
- **Split.** Two stacks with a fixed ratio (2/5 and 3/5, or 1/3 and 2/3). Becomes a stack below standard width.
- **Sheet.** A drawer from the right at standard width, full screen on mobile, the right pane of a split on ultrawide.

## Access layers

In order of weight. A page uses the lightest layer that holds the information.

| Layer | Component | Holds |
| --- | --- | --- |
| Tooltip | `NeoTooltip` on an `(i)` or on the value | one fact, one line |
| Popover | `NeoTooltip` with click | up to six facts, or a two field form |
| Expand | row toggle | a short section under the row |
| Panel | `NeoDrawer`, tabs | the whole record |
| Page | route | Chat, a bot, a repository |

## Row anatomy

```
 ●  Name                secondary phrase          fact   fact   [Action] ···
```

Leading status dot. Primary text at a fixed width. One secondary phrase at a fixed width. At most two trailing facts. One primary action. An overflow menu with the rest. On mobile the facts and the secondary phrase drop into the row's expand.

## Strings

| Kind | Width | Overflow |
| --- | --- | --- |
| name | 24ch | ellipsis, tooltip |
| repository | 40ch | ellipsis from the left, tooltip |
| address | 24ch | ellipsis, tooltip |
| id | first 8 characters | tooltip, copy |
| version | 12ch | ellipsis |
| message, error | 64ch, one line | tooltip, full text in the panel |
| number | 8ch, tabular figures, right aligned | never |
| time | relative, 9ch | absolute in tooltip |
| state | pill, 10ch | never |

## Field components

| Component | Data | Control |
| --- | --- | --- |
| Segmented | enum up to four values | `NeoButtonGroup` radio |
| Select | enum over four values, or options from one record | `NeoSelect` |
| MultiSelect | repeated enum | `NeoSelect` multiple |
| Checklist | up to eight options from one record | `NeoCheckbox` list |
| Picker | a daemon record | `NeoSelect` searching the daemon as typed, paginated |
| Switch | bool | `NeoSwitch` |
| Slider | number with min and max | `NeoRange` with a number box, unit shown |
| Number | number without bounds | `NeoNumberStep` |
| Text | string | `NeoInput`, max length and counter when bounded |
| Textarea | long string | `NeoTextarea` |
| Chips | repeated string | `NeoInput` adding `NeoPill` chips |
| KeyValues | map of strings | rows of two `NeoInput` |
| Path | file or directory | `NeoInput` with the directory browser |
| Duration | milliseconds or seconds with a max | `NeoNumberStep` with a unit `Segmented` |
| Bytes | a byte count | `NeoRange` against a known total, else `NeoNumberStep` with a unit |
| Percent | zero to a max | `NeoRange` |
| Secret | a stored secret | `NeoPassword`, redaction marker when set |
| Code | YAML or JSON | CodeMirror with validation |
| Cron | a five field cron | preset `Select` with a custom `Text` |

**Form layout.** Sections with a title and an optional `(i)`. Two fields per row at standard width and up, one per row on mobile. Advanced fields under one collapse per section. A save bar pinned to the bottom appears when the form is dirty.

**Pickers query the daemon.** The picker sends the typed text and a cursor. Nothing is filtered in the browser. Lists with a search box or filters do the same. These RPCs need `query`, `cursor` and `limit` before their pages ship, plus the filters named:

| RPC | Filters |
| --- | --- |
| `ListModels` | kind, runtime, format, sort |
| `ListInstances` | state |
| `ListSlots`, `ListRoutes`, `ListPools`, `ListObjectives`, `ListSchedulerConfigs` | state where one exists |
| `ListInstalls`, `ListBuilds` | runtime |
| `ListTasks` | kind, state |
| `ListTraces` | kind, status class, node, objective, fairness id, time range |
| `ListConversations` | |
| `ListBotActivity` | level, kind |
| `ListNodes`, `ListFormations` | state |
| `ListUsers`, `ListTokens`, `ListToolServers`, `ListSources` | |

Lists refetch their current page, debounced, when the live stream reports a change of their kind. Open panels update from the stream directly.

## Shared behaviour

**Live stream.** One `WatchEvents` with `snapshot: true` over every kind feeds counters, pills, open panels and list refetches.

**Tasks.** A call that answers with a `Task` raises a toast with a progress bar following `WatchTask`, linking to Tasks. The top bar shows the active count.

**Confirm.** Stop, evict, remove, revoke: `NeoPopConfirm` on the button. Force delete, forget node, rotate authority, reset mesh, delete bot: `NeoDialogConfirm` with the name typed.

**Conditions.** One row per condition: status dot, type, reason. Message and time in the tooltip. False first.

**Memory bars.** One bar per device. Segments: instances (solid), reservations (hatched), other processes (grey), free. Labels only in the tooltip. A legend once per page.

**Status colours.** Ready green. Starting, pending amber. Draining, stopping blue. Failed red. Idle, stopped grey.

**Empty, loading, error.** Loading draws skeletons in the layout the data takes. Empty states hold one line and one button. Failed calls toast the daemon's message.

**Shell.** Top bar: host label, mesh name when in one, tasks count, live dot, account menu. Rail collapses to icons and remembers. Mobile bottom bar: Overview, Chat, Serve, Traffic, More.

## Roles

| Area | inference | operator | admin |
| --- | --- | --- | --- |
| Chat, own password, own tokens | yes | yes | yes |
| Read | | yes | yes |
| Run, stop, swap, pull, install, put, delete | | yes | yes |
| Accounts, settings, sources, mesh membership | | | yes |

Pages a role cannot read are absent from the rail.
