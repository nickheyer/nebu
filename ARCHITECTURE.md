# Nebu Architecture

Nebu is a single Go binary. `nebu serve` runs the daemon. Every other subcommand, and the
web UI, talks to the daemon over Connect RPC. The daemon owns a model store, a set of runtime
installs, a set of slots, and an OpenAI-compatible gateway. Your router points at the gateway
once and never changes again.

## Principles

1. Zero cgo. `CGO_ENABLED=0` is enforced in the Makefile and CI. Hardware is probed through
   vendor CLIs and sysfs, never through bound libraries.
2. Proto is the API source of truth and nothing else. The database schema is
   `internal/db/schema.sql`, and atlas writes the migration directory from it, one init
   migration until release, applied and recorded at startup with atlas's own revision table.
3. Go is generic. Nothing in Go knows a model name, a GPU name, a vendor name, or a CUDA arch;
   that is data under `spec/`, decoded into proto messages and validated when it loads. Where a
   third party wire format needs code, it lives in one localized module behind a shared interface
   and nowhere else: a catalog adapter behind `sources.API`, a header reader behind
   `formats.Reader`. Everything above the interface is written once for every implementation.
4. The host is probed, never configured. Runtime constraints and build recipes are templates
   that the probed host profile fills in.
5. The runtime is the ground truth. Nebu asks it to list devices, dry-run a fit, or report what
   it allocated, and uses measured results to calibrate its own estimates.
6. Formats are a property of the artifact. Runtimes declare which formats they accept.
7. Launchers are plural behind one interface. A bare process is the default and the only
   one implemented so far. systemd units and OCI containers are alternatives, not the design.
   Build sandboxes follow the same rule: the host toolchain and an OCI CLI are two runners
   behind one interface.

## Nouns

- **Host**. Probed profile of the machine. Devices, memory pools, storage, facts.
- **Settings**. Host wide preferences as rows: the label people call the host by, whether the
  setup guide was dismissed. Edited from the settings page or `nebu host --label`.
- **Transport**. How bytes and listings move: HTTP, OCI distribution, filesystem, git with or
  without LFS, the Hugging Face CLI. One Go type each behind one interface.
- **Provider**. A hosted platform and the wire format it speaks: Hugging Face, Docker Hub,
  GitHub, Ollama, ModelScope, Civitai, Kaggle, NGC, CSGHub, a host filesystem, a nebu mirror. One
  Go module each, on one or more transports, implementing the catalog API.
- **Source**. A configured instance of a provider. Rows in the database, seeded or bootstrapped
  from config, created and edited in the UI. The only one of the three a user sees.
- **Model**. A repository at a revision. Owns artifacts.
- **Artifact**. A file or file set with a format and a role such as weights, projector,
  tokenizer, config, or draft.
- **Descriptor**. Format-neutral facts read from artifact headers without downloading weights.
  Architecture parameters, tensor groups with sizes, cache shape inputs.
- **Runtime**. A backend manifest. How to acquire it, launch it, probe it, estimate for it.
- **Install method**. One way a manifest says its runtime can be obtained: a binary on the host,
  a published release, or a recipe. Offered as a list, one chosen and configured when installing.
- **Install**. A concrete usable copy of a runtime. Adopted, downloaded, or built.
- **Recipe**. How to build an install. Base repo, ref, patches, flags, all templated.
- **Estimate**. A memory plan for a model on a runtime with given params on this host.
- **Slot**. A numbered reservation of devices and memory budget under one public name, and
  the model it is meant to serve.
- **Instance**. A running install serving a model, in a slot or under its own name.
- **Route**. A public model name mapped to an instance in the gateway.
- **Trace**. One request through the gateway: timing, tokens, and both bodies.
- **Task**. Any long-running operation with streamed progress and logs.
- **Build**. One run of a recipe on this host, keyed by the hash of everything that decides
  its bytes.
- **Event**. One change to any of the above, streamed to the UI.

## Tree

```
nebu/
+-- ARCHITECTURE.md                this document
+-- README.md                      intro and objectives
+-- Makefile                       gen, web, build, test, lint, cgo guard, release
+-- flake.nix                      dev shell with go, node, buf
+-- buf.yaml                       proto module, lint and breaking rules
+-- buf.gen.yaml                   go, connect-go, connect-es, openapi outputs
+-- go.mod
+-- cmd/
|   +-- nebu/
|       +-- main.go                single binary, dispatches serve and client subcommands
+-- proto/
|   +-- nebu/
|       +-- v1/
|           +-- config.proto       daemon and client configuration
|           +-- host.proto         host profile, devices, memory pools, facts
|           +-- settings.proto     host wide preferences
|           +-- source.proto       sources, search, resolve
|           +-- model.proto        model, revision, artifact, format, role, descriptor
|           +-- store.proto        stored models, pulls, verification, gc, export
|           +-- runtime.proto      runtime manifest, installs, params, triage, constraints
|           +-- instance.proto     running models, run, stop, logs
|           +-- recipe.proto       recipes, patches, sandboxes, builds
|           +-- estimate.proto     memory plan requests and results
|           +-- slot.proto         slots, swaps, eviction
|           +-- gateway.proto      routes, listeners, api flavors
|           +-- task.proto         durable jobs, progress and log streams
|           +-- event.proto        watch stream feeding live ui updates
+-- pkg/
|   +-- proto/nebu/v1/             generated go and connect-go, never hand edited
|   +-- config/                    daemon config, paths and listeners only
|   +-- logger/                    structured logging
|   +-- eval/                      expression and template engines every spec file uses
|   +-- cache/                     disk cache for listings and header reads
|   +-- events/                    in-process bus behind the watch stream
|   +-- spec/                      loads layered spec files into proto messages
|   +-- host/                      host profile assembly and fact evaluation
|   |   +-- probes/                generic exec, csv, json, kv, sysfs readers driven by spec
|   +-- sources/                   providers behind one catalog api, transports behind one byte interface, sources as rows
|   +-- mirror/                    index files an export writes and a mirror source reads
|   +-- formats/                   classifier, groups, and header readers
|   |   +-- gguf/                  header and tensor table from range reads
|   |   +-- safetensors/           shard headers plus config.json
|   |   +-- pickle/                the pickle opcodes PyTorch checkpoints use, decoded without executing anything
|   |   +-- torch/                 torch.save zips read for nemo, tensor names and shapes from data.pkl alone
|   |   +-- nemo/                  NeMo .nemo tars and NeMo 2 directories, config plus zarr or distributed checkpoint metadata
|   +-- descriptor/                format-neutral descriptor, tensor groups, arch params
|   +-- store/                     content-addressed blobs, manifests, stable link tree, gc
|   +-- transfer/                  resumable chunked downloads, verification, throttling
|   +-- archive/                   tar and zip extraction with traversal checks
|   +-- estimate/                  planner placing tensor groups and caches into memory pools
|   +-- runtime/                   manifest model, param schema, command rendering
|   +-- build/                     recipe engine, fetch, patch apply, hashed build cache
|   |   +-- sandbox/               build runners, host toolchain or oci cli
|   +-- proc/                      process trees started, found, and stopped the same way on every os, a console and job of our own on windows
|   +-- launch/                    process launcher, output files, adoption by pid
|   +-- triage/                    log pattern matcher producing hints and fixes
+-- spec/                          every runtime and model specific lives here, never in go
|   +-- embed.go                   go:embed of the directories below
|   +-- runtimes/                  one manifest per backend, llamacpp.yaml, vllm.yaml, sglang.yaml, nemo.yaml
|   +-- formats/                   format descriptors, roles, file patterns, which reader parses them
|   +-- archs/                     architecture families, cache shapes, attention variants
|   +-- recipes/                   build recipes as templates over host facts
|   +-- precisions/                words for a weight group by its bits per weight
|   +-- patches/                   unified diffs recipes reference by file
|   +-- probes/                    vendor tool invocations and output to fact mappings
|   +-- triage/                    failure patterns mapped to summaries and hints
+-- internal/
|   +-- daemon/                    wiring of all managers, startup recovery, shutdown
|   +-- inspect/                   resolve, describe, and plan a model before download
|   +-- pull/                      fetch, verify, link, manifest, and export a weight group
|   +-- installs/                  install methods as forms, adopt, download, or build runtime binaries, run probes
|   +-- instances/                 plan, launch, supervise, persist, recover, and route
|   +-- calibrate/                 learned overhead corrections per runtime and arch
|   +-- doctor/                    probes, runtimes, installs, recipes, sources, and store checked as a task
|   +-- db/                        pure go sqlite, schema.sql is the truth, atlas migrations, one file per area over shared row helpers
|   |   +-- migrations/            one init migration and its atlas.sum until release
|   +-- rpc/
|   |   +-- server.go              connect server, h2c, interceptors, auth, web ui mount
|   |   +-- services/              the proto services grouped by area, every handler one call and one reply
|   +-- tasks/                     task engine, progress fan-out, cancellation, stored history
|   +-- slots/                     slot manager, reservations, swaps
|   +-- gateway/                   openai, anthropic, and ollama flavors over one canonical chat, the route table, limits, the trace ring
|   +-- notify/                    posts failed instances to webhooks
|   +-- cli/                       client subcommands over connect by area, one parse and one print helper, table and json output
+-- web/
|   +-- nebu/                      sveltekit static app embedded into the binary
|       +-- embed.go               go:embed of dist with a single page fallback
|       +-- src/lib/proto/         generated connect-es client, never hand edited
|       +-- src/lib/               api client, live state fed by events, shared components
|       +-- src/routes/            serve, slots, instances, store, catalog, runtimes, requests, tasks, chat, host, settings
|       +-- static/                openapi output
+-- docs/                          user docs
+-- scripts/                       release and ci helpers
+-- .github/workflows/             ci with gen check, lint (vet, cgo guard, embedded spec test, web check), tests, build
```

## Sources

A **transport** moves bytes and listings: HTTP, OCI distribution, filesystem, git with LFS, the
Hugging Face CLI. Each is one Go type behind one interface with three methods, list, open, and
read, over a locator in the transport's own terms. Each declares the settings it reads, an
endpoint, a token variable, a directory, with a type. New transports are code.

A **provider** is a hosted platform and the wire format it speaks: Hugging Face, an OCI registry
with Docker Hub seeded, GitHub, any git host, Ollama, ModelScope, Civitai, Kaggle, NGC, CSGHub, a
host filesystem, a nebu mirror. Each is one Go module that names the transports it uses with the
defaults their settings take, and implements the catalog API: search, resolve, revisions, card,
open. Providers are code. There are no source spec files, because a provider is a protocol and a
protocol needs a program, not a table. The settings a provider accepts are the union of its
transports' settings, the primary transport owning the bare names and every other one prefixing
its own unless it stands in for the primary, as a mirror's directory does for its endpoint, and
the provider publishes that list through its capabilities so the UI renders the form without
knowing any provider by name.

A **source** is an instance of a provider with its own settings, a map of names to values the
provider validates on create and update: unknown names are refused, URLs need a scheme, variables
are environment variable names, required ones must be set. Sources are rows in the database. The
config file is an idempotent bootstrap: an entry creates the source when no source with that name
exists and otherwise leaves it alone. The web UI creates, edits, and removes sources. Providers
that work without configuration, such as Hugging Face, Docker Hub, and GitHub, get one seeded
default source under the provider's name. The manager in `pkg/sources` owns the rows behind a
small store interface the database satisfies: on start it creates config entries and then seeded
defaults whose names are absent, and it rebuilds the registry from the rows on every create,
update, and delete, so every consumer follows the change without a restart. A row whose client
cannot be built stays listed with its error instead of failing the daemon.

Users see sources and never providers or transports. The catalog merges every source of a
provider into one listing, shows which source a hit came from, and narrows to one source on
request. GitHub is also where runtimes come from: a recipe's latest ref and a prebuilt rule's
asset resolve through the seeded `github` source's releases, so one release parser serves both.

## Flows

**Inspect** is the pre-download fit table and the first milestone.

1. A source resolves a repo and revision to artifacts with sizes and sha256.
2. A format reader range-reads only headers and emits a descriptor.
3. The estimator places tensor groups and caches into the host's memory pools once per
   runtime and candidate param set, the runtimes being the installed ones that accept the
   group's format, or every compatible one when nothing installed does, at the configured
   context lengths capped at the model's own.
4. The result is a table of quant by runtime by context length, each marked fits, partial,
   or no, with the plan that produced it, once against the whole memory and once against what
   is free right now, since a run plans against free memory.

**Pull** runs as a task and is safe to interrupt at any point.

1. The inspector resolves and classifies the repo, and the caller names one weight group.
2. Each artifact is fetched in parallel chunks into a partial file beside the blobs, with a
   sidecar recording finished chunks. A second pull resumes from that sidecar.
3. The whole file is hashed and compared with the sha256 the source reported, then renamed
   into `blobs/sha256-<hex>`. Two quants that share bytes share one blob.
4. A relative symlink is created under `models/<source>/<repo>/<group>/` so the path a
   runtime is given never changes when blobs move.
5. The manifest under `manifests/` records artifacts, digests, link paths, the descriptor,
   and the commit. Local sources are hashed and hard linked instead of copied.

Progress, rate, and log lines stream over a Connect server stream, which the CLI renders in
place. `remove` drops the manifest and links, `store gc` drops blobs no manifest references,
and `store verify` rehashes blobs and deletes corrupt ones so the next pull repairs them.

**Runtime install** is one of the methods the manifest lists, chosen by the person installing:
adopt a binary already on the host, download a published release, or run a recipe. The daemon
describes each method's settings as fields with defaults for this host, the published build its
rules select or the recipe variant the host selects, and the request carries the method and the
settings, so the same form installs any runtime. A recipe is hashed with the resolved host
facts, so an unchanged recipe on an unchanged host is a cache hit.

**Build** runs as a task and produces an install of kind BUILT.

1. The host profile selects a variant, the first whose `when` holds and whose tools are on
   PATH, unless the request names one. Vars render in key order against the host env,
   the variant, and the ref, and request vars override them.
2. The ref resolves. `latest` asks the github source for the newest release. The recipe, variant,
   vars, ref, sandbox, image, selected host facts, and patch contents are hashed into the
   build id. A finished build with that id whose binary and install still exist is returned
   at once unless the request forces a rebuild.
3. The source arrives as an archive through the download cache or a git clone at the ref.
   Patches whose `when` holds apply through a pure Go unified diff applier that tolerates a
   hunk already present.
4. Steps run through a sandbox runner, the host toolchain or `podman run` style containers
   through whichever CLI is present, with the build directory mounted. Every line streams
   to the task and to `build.log` in the build directory.
5. Outputs are copied out of the tree, the binary is checked, and the install is recorded
   with the manifest probes run against it. Builds, like installs, are rows in the store.

**Run** is where the runtime becomes the ground truth. The loop has been closed on an RTX
3080 Ti: llama.cpp's own model and cache buffers match the plan to the byte, the overhead term
is fitted to what the card reported, and the two measured runs live under
`pkg/estimate/testdata/measured` as fixtures the planner is held to.

1. The stored manifest supplies the link paths and descriptor. The host is probed again and
   the planner runs against free memory, so a second model plans around the first.
2. Params layer in one order everywhere a plan is made: the manifest defaults, then the slot's
   defaults, then the request's own. The manifest types every param and refuses unknown names
   or values of the wrong type, so the web form is built from the manifest.
3. Solved params replace `auto`, the runtime package renders the command and environment
   from the manifest templates, which also see the descriptor, and a free loopback port is
   picked.
4. The process launcher starts it in its own process group with its output appended to a
   file under the data dir, follows that file into a ring, and polls the manifest's health
   check until it answers. The child is one tree on every OS through `pkg/proc`: a process
   group with a parent-death signal on Linux, a process group elsewhere on Unix, and a job
   object on Windows that ends with the daemon, so a runtime and whatever it spawned stop
   together and a restart finds a survivor by pid and command line.
5. The route table maps the public model name to the instance endpoint and the gateway
   proxies by that name. Routes are rows in the store.
6. Report rules parse the runtime's own allocation lines into measurements, and the device
   free-memory delta feeds the calibration table, which shifts the estimator's overhead
   term for that runtime and architecture on the next plan.
7. On failure or unexpected exit, and on a prepare step that fails, triage matches the
   output against the pattern catalog and the hint, with any suggested params, travels back
   on the task and the instance record. The correction recorded for the calibration table is
   measured against the plan without the correction it already carried, so it converges on
   what the card reported rather than halfway there.
8. Every state change writes the instance row, so `ps --all`, `show`, and `logs` answer for
   instances that ended before the daemon last started.

**Recovery** runs before the daemon listens. Tasks left unfinished are marked failed. For each
instance row that was not terminal, the daemon checks whether its pid is alive with the
recorded command line. A live runtime still wanted is adopted: its output file is followed from
where it is, it is routed as soon as it answers health, it is supervised by pid, and a stop
signals its group and escalates after the grace period. One whose stop was in flight is
finished instead. A runtime outlives the daemon only where nothing ties the two, on macOS and
FreeBSD, or when it ignored the parent death signal, since Linux and Windows end the tree with
the daemon, so there a crash means a relaunch rather than an adoption. A dead one is marked
stopped with the reason.
Every record still wanted, meaning it was never stopped by request and is not already live,
is relaunched from its original request one at a time, so each plans around the last. A stop
by request clears that intent; a daemon shutdown does not, which is why models survive a
reboot but not a `nebu stop`. Builds left running are marked failed. Slots pick up the live
instance bound to them and route it as soon as it answers health, and route rows come back
pending until an instance is adopted or relaunched.

**Installs** are adopted from a path or PATH, or downloaded through a prebuilt rule, the one
whose `when` holds on the probed host by default; every asset the rule lists is taken from one
release and unpacked into one directory. Manifest probes run the binary once to capture its
version and the devices it sees.

**Slots** reserve devices and a memory budget under one public name. A run bound to a slot
plans against the slot's device pools capped at the budget, inherits the slot's default
runtime and params, and can pin the process to the slot's devices through the launch env
templates. The slot's name is a route that lives as long as the slot: while nothing serves
it the gateway answers 503 with a retry hint rather than 404. Slots are numbered; the
position orders the list and the relaunch after a restart, and a slot can be renamed, the
route moving with it.

A slot owns what it is meant to serve. Its request is set by a run or a swap, cleared by an
evict or a stop, and kept when the occupant fails, so a failed slot can be relaunched from
the page or `nebu slots relaunch`. After a daemon restart every slot with a request and
no live occupant relaunches it in position order, one at a time; a slot that failed on
its own before the restart stays failed with its error. Instances outside a slot relaunch
themselves as before.

**Swap** is run against an occupied slot. The new model is planned with the old one still
running. When it fits, the new instance starts beside the old one, the route flips once
health passes, and the old instance drains, taking no new requests while in flight ones
finish, then stops. When it does not fit, or the caller asks, the old instance drains and
stops first, the route goes pending, and the new instance starts, with the old request
replayed as a rollback if the new one fails. The public name never disappears.

**Traces** are the gateway's record of every request it proxies: the public name, the
client's wire format and the runtime's, whether it was translated, when the runtime's
headers came, when the first token came, when it ended, the status, prompt and completion
tokens, the stop reason, tool calls, and both bodies capped at 64 KiB. A passed through
answer is read on its way past through the upstream flavor's own parser, so tokens and
timing are the same whichever format the client spoke. The newest five hundred live in a
ring in memory, each reaching the event stream without its bodies when it starts and when
it ends, and the response carries the trace id in `X-Nebu-Trace`. `GatewayService.ListTraces`
and `GetTrace` answer the requests page, the slot and instance pages, and the chat console,
which correlates every answer with its trace.

**Doctor** probes the host again and checks probes, devices, storage, the store, runtimes,
recipes, and sources, as a task whose log holds one line per check and which fails when any
check does. It runs once when the daemon starts and again from the host page or `nebu doctor`.

**Events** are published by every manager on every change and fan out through an in-process
bus to `EventService.WatchEvents`, a Connect server stream that starts with a snapshot. The
web UI holds one subscription and renders from it, so nothing polls.

## Conventions carried over from discopanel

- `proto/**/*.proto` is the API. `make gen` regenerates Go, connect-go, connect-es, and the
  OpenAPI document. Generated code is never hand edited.
- Buf v2 with remote plugins, so no local protoc.
- SvelteKit with adapter-static, Tailwind, bits-ui, embedded via `go:embed`.
- Connect over h2c on one listener. The gateway shares that listener by default under `/v1/`
  and moves to its own address when `gateway.listen` is set in config. The web UI is served
  from the same listener at `/`.
- Services under `internal/rpc/services` are grouped by area, catalog, runtimes, and serving, every
  handler one manager call answered through one reply helper that maps errors onto codes.
- A bearer token on the API when `auth.token` is set, checked by one interceptor for unary
  and streaming calls. Gateway keys are separate under `gateway.api_keys`.
- TLS with HTTP/2 on every listener when `tls.cert_file` and `tls.key_file` are set, and a
  warning at start for a listener beyond loopback without TLS, a token, or keys.
- Every gateway path answers CORS preflights, narrowed to `gateway.cors_origins` when set.
- One `Policy` of in flight cap, rate, burst, request timeout, and upstream timeout on the
  gateway, every slot inheriting the fields it leaves at zero.

## Deliberate departures

- No protogorm. Proto stops at the API boundary. The store is SQL.
- No Docker SDK. Containers are one launcher among three and are driven through the CLI so
  podman and nerdctl work unchanged.
- No websocket hub. Live UI updates ride a Connect server stream, which works in browsers
  over HTTP/1.1 without a proxy.
- No cgo SQLite driver. The modernc pure-Go port. Installs, builds, instances with their
  plans, measurements, triage, and requests, slots, routes, calibrations, and task history are
  rows in `<data_dir>/nebu.db`, created by the embedded migrations.
  Runtime output and build transcripts are the things kept as files, truncated past a size
  cap for runtimes.
- No `patch` binary and no git library. Recipe patches apply through a small unified diff
  applier in Go, and git is only shelled out to when a recipe clones instead of downloading
  an archive.
- Spec files are YAML that unmarshal through protojson into the same messages the API uses.
  One schema, two transports.

## Spec file contract

Every file under `spec/` is YAML that unmarshals through protojson into one message from
`proto/nebu/v1`. Unknown fields fail the load. A site adds or overrides any spec by dropping
a file with the same `id` into a directory listed in `spec_dirs`, which always includes
`<data_dir>/spec`.

- `probes/` is `ProbeSpec`. Go knows how to exec a command or read a file, parse CSV, JSON,
  key-value blocks, or a regex, and render templates. Which tool, which fields, and which
  facts come out is the spec.
- `formats/` is `FormatSpec`. File rules claim paths and assign roles, group rules name the
  loadable set, tensor rules classify tensor names into placement kinds, param rules map
  metadata keys onto descriptor params with optional derive expressions or sum the elements of
  matching tensors. `reader` names the Go parser, so several formats can share one, and `root`
  attaches files across a directory tree. A weight whose format requires files the repository
  lacks falls through to the next format that claims it.
- `archs/` is `ArchSpec`. A regex over the architecture name and formulas such as
  `cache_per_token` evaluated with the descriptor params and the run params in scope. Formulas
  may use one another in any order, so a sliding window family declares which layers keep the
  window and folds `n_ctx` and `n_swa` into one per token figure. A family claims a checkpoint
  only when its formulas evaluate over the params read from it, run params guarded with `??`, so
  a checkpoint missing what the family needs falls to the next match rather than failing to plan.
- `runtimes/` is `RuntimeManifest`. Accepted formats, host constraints as expressions,
  acquisition including the recipe id, launch templates with an optional prepare step, typed
  params with their flags, the estimate policy, and report rules for calibration. Every param
  carries what a form needs to render it without knowing the runtime: a label, a unit, bounds
  and a step for numbers, choices for a fixed set, the group it sits under, and whether it is
  advanced. The web UI renders every runtime's parameters from these fields alone.
- `recipes/` is `Recipe`. Source as a releases repository at a source, ref, archive, or repo
  templates, patches
  with conditions, variants selected by host expressions with their tools and vars, the
  sandbox, steps as templated argv, outputs, and the binary.
- `patches/` holds unified diffs a recipe patch names through `file`.

Expressions use expr syntax with `KiB`, `MiB`, `GiB`, `vercmp()`, and `num()` in scope.
Templates use Go text/template with `missingkey=error`, so optional fields go through
`index`, plus string helpers such as `join`, `replace`, `trimPrefix`, and `default`. Launch
templates also see `.devices`, the slot's devices with their facts, empty without a slot, and
`.descriptor`, the stored model's format, architecture, params, and metadata, so a manifest can
pick a flag by what the checkpoint holds.

## Planner

The estimate policy is the only place a runtime's memory behaviour is described. Each
tensor group kind maps to a pool and, when offloadable, to the param that counts it. Device
capacity is the sum of every device pool for a runtime that spreads layers across devices,
or the largest pools up to the count a param names when the policy sets `devices_param`, so
vLLM at tensor parallel one plans on one card. A slot's budget caps each of its device pools
rather than their total, because runtimes allocate per device and a total could not say which
card overflows. The planner puts fixed kinds where the policy says, then searches offload
counts in spill priority order, most protected kind outermost, taking layers from the end
first. A kind
that `requires` another can only sit on device where its parent does. Cache bytes follow
the layer kind, overhead sits on device, and the solved counts come back as params ready
to render as flags. The verdict is FITS when every offloadable item is on device, PARTIAL
when some spilled, and NO when the fixed need alone does not fit.

A kind whose policy carries a `when` expression loads only while it holds over the run
params and header facts, and otherwise stays on disk, listed under the plan's `skipped`
rather than in any pool: the prediction heads a checkpoint ships count on SGLang and vLLM
once a speculative method names them and never on llama.cpp. Placements carry weights alone,
so a pool's remainder over its placements is the cache and overhead it holds.

A plan with no fit is still laid out, the way the host would take it: device pools fill to
their capacity in order, the rest flows on into host memory, and whatever no pool holds runs
the last pool past its capacity. The pools then say the shortfall themselves, a 12 GiB card
reading 12 of 12 and the 63 GiB beside it 63 plus what is left over, which is what the app
draws, and the solved counts say what landed on device so a forced run starts from them.

Tensor kinds are what the format specs classify: embedding, layer, experts, output, vision
and audio encoders with their projectors, draft heads, and other. Encoder and draft rules
sit ahead of the layer rules, since each numbers layers of its own, and a format's
`draft_from` expression names the first layer index that is a draft head rather than a main
layer, so a checkpoint that numbers its heads after the layers its config counts keeps
them apart. The GGUF reader folds the projector a run loads beside the weights into the
tensor table, so a vision tower counts wherever it lives.

## Milestones

1. `nebu doctor` and `nebu inspect`. Host probes, sources, format readers, estimator. Ships as a
   CLI with no daemon state beyond a cache.
2. `nebu pull`. Store and transfer with resume and verification. Task history lives in the
   daemon, so `--detach` and `tasks` need `nebu serve` running.
3. `nebu run`. Installs, the process launcher, gateway, triage, calibration, the SQL store,
   recovery with adoption and relaunch. Instances live in the daemon, so `run`, `ps`, `show`,
   `stop`, and `logs` need `nebu serve`, which the CLI finds on the configured listen address.
4. `nebu build`. Recipes over host facts, patch sets, host and OCI sandboxes, the hashed build
   cache, builds as rows next to installs.
5. Slots, swaps, the route table, gateway keys and API token, the event stream, store export,
   the mirror source, and the SvelteKit app embedded in the binary.
6. Request traces, the requests page, and the chat console as a debugging surface for a
   running model: every answer with its timing, tokens, trace, and the runtime's log beside it.

All six have code. The slot manager, the gateway, the trace ring, the route table, the store,
and the database each have their own tests. Nothing here has been verified against a second
real runtime beyond llama.cpp; the vLLM, SGLang, and NeMo manifests are held to the same
generic code paths through their specs alone.
