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

- dashboard, devices with memory bars, slots as drop targets, running instances, tasks
- catalog, search a source, inspect a repository, see the fit table, pull a group, watch it
- store, stored models, run or swap through a dialog, drag a row onto a slot, gc, verify, export
- runtimes, manifests with compatibility, adopt, install prebuilt, build with a recipe, installs, builds
- slots, create, evict, delete, drop a model, follow the occupant's log
- instances, list, stop, plan, measurements, triage, live log
- tasks, list and follow any task
- monitor, watches, findings, check now
- host, profile and doctor
- settings, api token, gateway listeners and routes, aliases

When `auth.token` is set, enter it on the settings page. It is kept in the browser only.

For development, `npm run dev` in `web/nebu` proxies API calls to `NEBU_ADDR` or
`127.0.0.1:8484`.
