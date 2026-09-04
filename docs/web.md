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

- overview, device and host memory meters, slots as drop targets, running instances, activity,
  unacknowledged findings, and a get started checklist until a model is serving
- catalog, one interface over every configured source, an All tab that searches every source at
  once with each provider's order interleaved, then one tab per provider: a provider with several
  sources merges them into one listing, marks every card with the source it came from, and
  offers a row of source chips to narrow to one. Browse without a query in the
  provider's default order, search, sort, flip the order where the provider allows it, and narrow
  by the facets it declares such as task, library, license, type, or capability, with cards or a
  list and endless paging. Every card says in words what it shows, which runtime can serve the
  model, and whether a token is needed. Typing a repository name inspects it directly. A model opens in a drawer that lists its weight groups as builds to choose
  between, with the precision of each explained, the download size, whether it fits on this host,
  and pull progress, then a fit matrix of group by context length per runtime with the plan behind
  every cell, each cell carrying two verdicts, one against the whole device memory and one
  against what is free right now with everything already loaded, the model card, and its
  revisions, tags, versions, or variants to switch between, plus watch and pull
- store, stored models with a slot rail to drop them on, run or swap through a dialog that can
  check the memory plan first, export one or all as a mirror, verify, collect garbage, remove.
  The run dialog is a typed form built from the runtime's manifest: every param with its type,
  choices, and description, a profile to start from, and empty fields that inherit the profile
  and then the slot, so the form only carries what this run changes, and a run anyway switch for
  a model the plan says does not fit. The store's totals and each model's last use follow the
  stream
- runtimes, manifests with compatibility and unmet constraints, adopt, install prebuilt, build
  with a recipe, profiles with add, edit, make default, and remove through the same typed form,
  installs, builds with their logs
- slots, cards with state and route counters, a drawer with the occupant, reservation, the
  limits its route enforces, last request, live log, history, and a chat on it while it serves,
  create, edit, evict, delete, run or swap
- instances, running, failed, or all, a drawer with overview, plan, live log, and triage hits
  that can relaunch with the suggested fix
- tasks, all, active, or failed with progress, a drawer following the log with cancel
- monitor, wanted models searched for across every source until one turns up, watches with
  check now, findings with acknowledge, each naming the want or watch it belongs to and its
  source, with the pull and the swap it set off, narrowed to one want or watch on request,
  watch a repository, want a model
- gateway, the endpoint with a copyable example, routes with state and counters, aliases
- chat, a debug conversation with any ready route through the gateway itself, streamed, with a
  system prompt, temperature, a token cap, and tokens per second per answer, reachable from a
  running instance's drawer
- host, doctor, devices with facts, memory pools, storage, probes, host facts
- settings, sources with add, edit, and remove through a form the daemon describes, the api token,
  the connection, and desktop notifications for findings

In the catalog, `/` focuses the search box and Enter on a repository name opens it without
searching. Errors surface as toasts, every new finding does too with a link to the monitor page,
destructive actions confirm first, and details open in a side drawer
that deep links through `?id=` so a task or instance can be shared by URL. Dropping a stored
model on an occupied slot swaps after confirming.

When `auth.token` is set, enter it on the settings page. It is kept in the browser only.

For development, `npm run dev` in `web/nebu` proxies API calls to `NEBU_ADDR` or
`127.0.0.1:8484`.
