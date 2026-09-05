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

Pages:

- overview, the host under its label with the hostname beneath, a setup strip until something
  serves or it is dismissed, where the label is first set, memory as one row per accelerator and
  one for host memory, slots as bays that take a dropped model, running instances, activity, and
  unacknowledged findings
- catalog, a rail of sources on the left, All sources first and then every provider, the ones
  that list before the ones that only open a typed name, a provider with several sources unfolding
  them beneath it, and a table on the right: search, the provider's sorts on the columns that
  carry them, its facets as filters, endless paging, skeleton rows while a page loads, a lock on a
  gated repository, and a mark on what is already stored. A model opens in the side panel, which
  every page shares at the same width and which drags wider: a Weights tab with each weight group's
  precision, size, and whether it fits this host or one slot, a pull or run button per row, a fit
  matrix by context length with the plan behind every cell, the model card, and its revisions
  when it has more than one. A gated repository on a source without a token shows what the
  daemon needs instead of asking the source
- store, stored models with a slot rail to drop them on, run or swap through a dialog that can
  check the memory plan first, export one or all as a mirror, verify, collect garbage, remove.
  The run dialog is a typed form built from the runtime's manifest, a profile to start from, and
  empty fields that inherit the profile and then the slot
- runtimes, manifests with compatibility and unmet constraints, adopt, install prebuilt, build
  with a recipe, profiles with add, edit, make default, and remove, installs, builds with their logs
- slots, the same bays as the overview, a drawer with the occupant, reservation, the limits its
  route enforces, last request, live log, history, and a chat on it while it serves
- instances, running, failed, or all, a drawer with overview, plan, live log, and triage hits
  that can relaunch with the suggested fix
- tasks, all, active, or failed with progress, a drawer following the log with cancel
- monitor, wanted models searched for across every source until one turns up, watches with
  check now, findings with acknowledge, each naming the want or watch it belongs to
- gateway, the endpoint with a copyable example, routes with state and counters, aliases
- chat, a conversation with any ready route through the gateway itself, streamed
- host, doctor, memory rows with each device's facts, memory pools, storage, probes, host facts
- settings, the host label, the api token and connection, sources with add, edit, and remove
  through a form the daemon describes, and desktop notifications

In the catalog, `/` focuses the search box and Enter on a repository name opens it without
searching. Errors surface as toasts, every new finding does too with a link to the monitor page,
destructive actions confirm first, and details open in the side panel, which deep links
through `?id=` so a task or instance can be shared by URL. Dropping a stored
model on an occupied slot swaps after confirming.

When `auth.token` is set, enter it on the settings page. It is kept in the browser only.

For development, `npm run dev` in `web/nebu` proxies API calls to `NEBU_ADDR` or
`127.0.0.1:8484`.
