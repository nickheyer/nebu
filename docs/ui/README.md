# nebu web UI

Operator console for a nebu daemon.

Visual identity, palette and type: [style.md](style.md).

## Pages

Nine rail entries. A group page carries its tabs on routes of their own.

| Page | Tabs and routes | Roles |
| --- | --- | --- |
| [Login](login.md) | `/login` | everyone |
| [Overview](overview.md) | `/` | operator, admin |
| [Chat](chat.md) | `/chat`, `/chat/<id>` | everyone |
| Models | [Library](models.md) `/models`, [Discover](discover.md) `/discover`, `/discover/<source>/<repo>` | operator, admin |
| Serve | [Instances](serve.md) `/serve`, [Runtimes](runtimes.md) `/runtimes` | operator, admin |
| Gateway | [Traffic](traffic.md) `/traffic`, [Routes, Pools, Objectives, Schedulers, Imports](gateway.md) `/gateway/<tab>` | operator, admin |
| [Mesh](mesh.md) | `/mesh/<tab>` | operator reads, admin manages |
| [Bots](bots.md) | `/bots`, `/bots/<id>/<tab>` | operator, admin |
| Host | [Overview, Logs](host.md) `/host`, `/host/logs`, [Tasks](tasks.md) `/tasks` | operator, admin |
| [Settings](settings.md) | `/settings/<tab>` | admin, own tokens for everyone |

## Rules

1. A view shows only what the average case needs. Everything else sits one access layer away.
2. Nothing scrolls horizontally. Facts that do not fit a row move to an access layer, not to a longer row and not to a taller one.
3. Every field is typed to its data: a fixed set becomes a choice control, a bounded number becomes a range control, a daemon record becomes a searchable picker that queries the daemon. No free text where a choice exists.
4. Every string container has a width for its longest expected value and truncates with an ellipsis, the full value in a tooltip.
5. Space is balanced. Every block of a page sits in a card, cards fill the frame, and a detail panel exists only while a record is open.
6. Every page is specified for three widths.
7. Grid where columns must line up, a list's cells and its head, card columns, the pipeline. Stacks and rows elsewhere.
8. Standard patterns only: card, list with a column head, master and detail, tabs, stepper, toast, confirm dialog, empty state with one action. Labels, never sentences, in the UI, a hint under an empty state excepted.

## Widths

| Name | Width | Shell | Detail |
| --- | --- | --- | --- |
| Mobile | up to 720px | bottom bar, five tabs and More | full screen sheet |
| Standard | 721px to 1599px | left rail, labels on | beside the list once the page is 1080px wide, else a 560px drawer |
| Ultrawide | 1600px and up | left rail | beside the list, the list filling the page while none is open |

The top bar and the page share one frame, 1760px at most, centred in the space beside the rail, so titles, tabs and cards start on one edge.

**List pane.** A list lays out by its own width. Below 640px the secondary phrase and the facts move into the row's expand. Below 720px a facts strip wraps two per row and memory bars stack the name over the track.

## Layout primitives

- **Card.** The raised surface every block of a page sits in. A title row with a count, an `(i)` and quiet tools, then the body, flush for lists and screens.
- **List.** Rows sharing one set of columns under a head naming them, inside a flush card.
- **Columns.** Two columns of cards in a fixed ratio, one column under 1000px.
- **Toolbar.** Compact controls over a list, the search taking the free width.
- **Sheet.** The list and the open record, beside it, in a drawer, or full screen by width.
- **Stack**, **Row.** Inside cards and panels.

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

Leading status dot. Primary text. One secondary phrase. At most two trailing facts. At most one action, a text button naming a verb other than open. An overflow menu with the rest. Clicking the row opens its record, so no row carries Open or Edit. Every row of a list lines up with the head. On mobile the facts and the secondary phrase drop into the row's expand. A list row is 44px over a hairline, a fact row 32px.

**Filters.** A filter Select carries its name as its placeholder, the sort Select a sort icon before its value. No label stands beside a filter.

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

**Form layout.** Sections with a title and an optional `(i)`. Two fields per row at standard width and up, one per row on mobile. Advanced fields under one collapse per section, nothing after it. A save bar pinned to the bottom appears when an edit form is dirty and stays from the start on a create form. An unset number reads Default, a seed Random.

**Creating.** A record a page lists is created and edited in the same panel that shows it. A create of three fields or fewer is a dialog. A multi step create is a stepper dialog.

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

**Memory bars.** One bar per pool: the label at 5ch, the device name at 24ch with an ellipsis from the right, the track, used over total at 14ch, every track in a section on the same x. A device living on another pool's memory, an integrated GPU on system memory, is one more row in the same anatomy reading shared memory where the track would be, on every page that draws the bars. Segments: instances (solid), reservations (hatched), other processes (grey), free. Labels only in the tooltip. A legend once per page.

**Counts.** A card title or tab label carries the count of rows its list holds, 13px muted after the label. It appears once the list has loaded, zero included, and is absent while loading. The tabs of one page get their counts together.

**Status colours.** Ready green. Starting, pending amber. Draining, stopping blue. Failed red. Idle, stopped grey.

**Empty, loading, error.** Loading draws skeletons in the layout the data takes. An empty card holds one centred block, an icon, a line, a one line hint saying what fills it, and at most one button leading to the nearest action, the accent when it is the page's next step. An optional card with nothing in it is left out. A quiet one line form serves a row's expand. Nothing stands in for a closed panel. Failed calls toast the daemon's message.

**Shell.** Top bar: the page's title, meta, actions and tabs, then the tasks count, live dot and account menu. Rail: the mark, the host label and the mesh name when in one, then the pages. The host label is asked for directly at sign in and kept through navigation, the stream only updating it. Rail items are text buttons, 38px, weight 500, icon then label, the active one in primary text on an inset pad. The rail collapses to 60px of icons with the label in a tooltip and remembers. Mobile bottom bar: Overview, Chat, Serve, Gateway, More, the meta and actions on a line under the title.

**Page title.** Every page carries its title in the top bar, the rail entry's name for a page and its tabs. A record page, a bot or a repository, carries the record's name after a back link. Nothing in the page body repeats it.

**Screenshots.** `make screenshots` renders every page at the three widths and 2560px in both themes, the login page signed out, and one open panel, drawer, dialog or wizard per page into `screens/`.

## Roles

| Area | inference | operator | admin |
| --- | --- | --- | --- |
| Chat, own password, own tokens | yes | yes | yes |
| Read | | yes | yes |
| Run, stop, swap, pull, install, put, delete | | yes | yes |
| Accounts, settings, sources, mesh membership | | | yes |

Pages a role cannot read are absent from the rail.
