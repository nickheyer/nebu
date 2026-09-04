# Web UI

`nebu serve` serves the web UI on the API listener. It is a SvelteKit static app under
`web/nebu`, built by `make web` into `web/nebu/dist` and embedded into the binary by
`make build`. A binary built without it answers `/` with a short note instead.

The UI uses the same Connect API as the CLI through the generated connect-es client under
`web/nebu/src/lib/proto`. One server stream, `EventService.WatchEvents`, feeds every page:
the daemon publishes a change to a task, instance, slot, route, install, build, stored
model, watch, or finding, and the page updates without polling. The stream starts with a
snapshot and reconnects with backoff.

Pages:

- overview, device and host memory meters, slots as drop targets, running instances, activity,
  unacknowledged findings, and a get started checklist until a model is serving
- catalog, one interface over every configured source, each a tab across the top: browse without
  a query in the source's default order, search, sort, flip the order where the source allows it,
  and narrow by the facets the source declares such as task, library, license, type, or
  capability, with cards or a list and endless paging. Every card says in words what it shows,
  which runtime can serve the model, and whether a token is needed. Typing a repository name
  inspects it directly. A model opens in a drawer that lists its weight groups as builds to choose
  between, with the precision of each explained, the download size, whether it fits on this host,
  and pull progress, then a fit matrix of group by context length per runtime with the plan behind
  every cell, the model card, and its revisions, tags, versions, or variants to switch between,
  plus watch and pull
- store, stored models with a slot rail to drop them on, run or swap through a dialog that can
  check the memory plan first, export one or all as a mirror, verify, collect garbage, remove
- runtimes, manifests with compatibility and unmet constraints, adopt, install prebuilt, build
  with a recipe, installs, builds with their logs
- slots, cards with state and route counters, a drawer with the occupant, reservation, last
  request, live log, and history, create, edit, evict, delete, run or swap
- instances, running, failed, or all, a drawer with overview, plan, live log, and triage hits
  that can relaunch with the suggested fix
- tasks, all, active, or failed with progress, a drawer following the log with cancel
- monitor, watches with check now, findings with acknowledge, watch a repository
- gateway, the endpoint with a copyable example, routes with state and counters, aliases
- host, doctor, devices with facts, memory pools, storage, probes, host facts
- settings, api token and connection

In the catalog, `/` focuses the search box and Enter on a repository name opens it without
searching. Errors surface as toasts, destructive actions confirm first, and details open in a side drawer
that deep links through `?id=` so a task or instance can be shared by URL. Dropping a stored
model on an occupied slot swaps after confirming.

When `auth.token` is set, enter it on the settings page. It is kept in the browser only.

For development, `npm run dev` in `web/nebu` proxies API calls to `NEBU_ADDR` or
`127.0.0.1:8484`.
