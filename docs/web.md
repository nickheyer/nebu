# Web UI

`nebu serve` serves the web UI on the API listener. It is a SvelteKit static app under
`web/nebu`, built by `make web` into `web/nebu/dist` and embedded into the binary by
`make build`. A binary built without it answers `/` with a short note instead.

The UI uses the same Connect API as the CLI through the generated connect-es client under
`web/nebu/src/lib/proto`. One server stream, `EventService.WatchEvents`, feeds every page:
the daemon publishes a change to a task, instance, slot, route, install, build, stored
model, the store's totals, watch, finding, want, source, or profile, and the page updates without
polling. Route counters arrive at most once a second while requests flow. The stream starts with
a snapshot and reconnects with backoff.

## Layout

A page is a title, the facts beside it, the actions that belong to the whole page, and then
sections. A section is a heading, a count or a word of context, its own actions, and its
content. Tables sit edge to edge under their heading. Boxes are kept for objects that are
boxes: a slot is a bay with a state rail down its left edge, a dialog, the side panel, a code
block, a log. State is a colored dot and a word. Explanations live behind an info mark beside
the thing they explain, never as prose under a label.

The side panel is shared by every page and opens at the same width everywhere, dragged wider
by its edge, the width kept per browser. It deep links through `?id=` on tasks and runtimes,
`?slot=` and `?instance=` on the serve page, `?connect=1` for the connect dialog, and
`?model=` on the library, so any of them can be shared by URL.

The sidebar carries the mark, the host by its label with the hostname or platform beneath, a
dot for whether the daemon answers, and eight destinations in three groups: what serves, what
runs in the background, and the machine itself. The old `/slots`, `/instances`, and `/gateway`
addresses forward to the serve page.

## Pages

- serve, the home page. The gateway endpoint with a copy button, a guide of the three steps
  from a fresh host to a model answering that marks itself off and can be dismissed, a line
  for unread findings, what the daemon is busy with, one bar per accelerator and one for host
  memory with a probe button, and the slots as bays. A bay carries the slot's name, its state,
  the model and runtime it serves, uptime, requests, measured memory, chat while it answers,
  swap or run as its one action, edit, and a menu. Instances running outside any slot sit in
  the same list. Instances that stopped or failed are listed beneath with the triage summary, a
  failed filter, and run again. Connect opens a dialog with every listener address, a request
  in the OpenAI, Anthropic, or Ollama dialect, the auth and limit defaults, every name the
  gateway answers to with its state and counters, and an alias form. A slot opens in the side
  panel with what it serves, its reservation, the limits its route enforces, the last request, a
  live log, and its history. An instance opens with its facts, parameters, measurements,
  command, memory plan, live log, and triage hits that relaunch with the suggested fix
- models, one page with two tabs. The library is what this host holds: disk use against the
  cap or the filesystem the store sits on, and a sortable table of stored models with a source
  column once two sources hold models. A row opens the model in the side panel. Run opens the
  run dialog, and the row menu runs or swaps into any slot in one click, exports, verifies, or
  removes. A maintain menu verifies everything, collects garbage, or exports the whole store as
  a mirror. Discover is the catalog: a rail of sources on the left, All sources first and then
  every provider, the ones that list before the ones that only open a typed name, a provider
  with several sources unfolding them beneath it, and a table on the right with search, the
  provider's sorts on the columns that carry them, its facets as filters, endless paging,
  skeleton rows while a page loads, a lock on a gated repository, and a mark on what is already
  in the library. A gated repository on a source without a token opens to what the daemon needs
  instead of asking the source
- the model panel, shared by both tabs: a table of weight groups with precision, size,
  parameters, whether the group fits this host or one slot and how that changes against free
  memory, the largest that fits marked, and pull or run. Beneath it a fit matrix by context
  length with the plan behind every cell, warnings folded away, the other files folded away,
  the model card, and its revisions when it has more than one, with a watch button in the footer
- the run dialog, opened from the library, a slot, or the serve page: the model, where it
  runs as a list of standalone and every slot with what a swap replaces, the runtime, install,
  profile, and model name, the parameters folded away under a count of what is set, inheriting
  the profile and then the slot, and the memory plan against free memory as the inputs settle.
  A plan that says no holds the run until it is forced, and a swap chooses between overlapping
  the models and draining first
- chat, a conversation with any ready route through the gateway itself, streamed, the model
  picked in the header, and the system prompt, temperature, max tokens, and gateway key behind
  an options button
- tasks, all, active, or failed as tabs over the table with progress, cancel from the row, a
  panel following the log
- monitor, findings as an inbox with acknowledge, each row naming the want or watch it
  belongs to and narrowed to one from that row's menu, wants searched for across every source
  until one turns up, and watches with check now, each added from its own dialog and saying in
  one column what happens on a match
- runtimes, a bay per manifest with its state, installed with its newest version, not
  installed, incompatible with the unmet constraints, or the download or build under way, every
  way to install it as a button while it has no install, and a menu to adopt, install prebuilt,
  build with a recipe, or add a profile, then profiles with add, edit, make default, and remove,
  installs, and builds with their logs. A runtime opens in the side panel
- host, doctor with what needs attention first and the passing checks folded away, devices
  that unfold what the probes reported, memory pools, storage, probes, host facts
- settings, rows of label and control: the host label, the api token and connection, sources
  with add, edit, and remove through a form the daemon describes, and desktop notifications

Every list renders skeleton rows in the shape of its table until the snapshot arrives, so no
page flashes an empty state on load. In the library and the catalog, `/` focuses the search
box, and Enter on a repository name in the catalog opens it without searching. A choice with
one option is shown as text rather than a select. Errors surface as toasts, every new finding
does too with a link to the monitor page, and destructive actions confirm first.

When `auth.token` is set, enter it on the settings page. It is kept in the browser only.

Inter and JetBrains Mono ship with the app through the fontsource packages, so the UI reads
the same on every host.

For development, `npm run dev` in `web/nebu` proxies API calls to `NEBU_ADDR` or
`127.0.0.1:8484`.
