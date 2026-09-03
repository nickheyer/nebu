# Nebu Architecture

Nebu is a single Go binary. `nebu serve` runs the daemon. Every other subcommand, and the
web UI, talks to the daemon over Connect RPC. The daemon owns a model store, a set of runtime
installs, a set of slots, and an OpenAI-compatible gateway. Your router points at the gateway
once and never changes again.

## Principles

1. Zero cgo. `CGO_ENABLED=0` is enforced in the Makefile and CI. Hardware is probed through
   vendor CLIs and sysfs, never through bound libraries.
2. Proto is the API source of truth and nothing else. The database schema is SQL migrations.
3. Nothing in Go knows a model name, a GPU name, a vendor name, or a CUDA arch. All of that is
   data under `spec/`, validated against proto messages at test time. A grep of the Go tree for
   any of those returns nothing.
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
- **Source**. Where models come from. Hugging Face, ModelScope, a mirror, a local directory.
- **Model**. A repository at a revision. Owns artifacts.
- **Artifact**. A file or file set with a format and a role such as weights, projector,
  tokenizer, config, or draft.
- **Descriptor**. Format-neutral facts read from artifact headers without downloading weights.
  Architecture parameters, tensor groups with sizes, cache shape inputs.
- **Runtime**. A backend manifest. How to acquire it, launch it, probe it, estimate for it.
- **Install**. A concrete usable copy of a runtime. Adopted, downloaded, or built.
- **Recipe**. How to build an install. Base repo, ref, patches, flags, all templated.
- **Estimate**. A memory plan for a model on a runtime with given params on this host.
- **Slot**. A reservation of devices and memory budget.
- **Instance**. A running install serving a model in a slot.
- **Route**. A public model name mapped to an instance in the gateway.
- **Task**. Any long-running operation with streamed progress and logs.
- **Build**. One run of a recipe on this host, keyed by the hash of everything that decides
  its bytes.
- **Watch**. A repository the monitor checks for new revisions and weight groups.
- **Finding**. One change a check noticed, with the task it triggered.
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
|           +-- source.proto       sources, search, resolve
|           +-- model.proto        model, revision, artifact, format, role, descriptor
|           +-- store.proto        stored models, pulls, verification, gc, export
|           +-- runtime.proto      runtime manifest, installs, params, triage, constraints
|           +-- instance.proto     running models, run, stop, logs
|           +-- recipe.proto       recipes, patches, sandboxes, builds
|           +-- estimate.proto     memory plan requests and results
|           +-- slot.proto         slots, swaps, eviction
|           +-- gateway.proto      routes, listeners, api flavors
|           +-- monitor.proto      watches and findings
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
|   +-- sources/                   source interface, search, resolve, range reads
|   |   +-- huggingface/           hub api, tree with sha256, download urls
|   |   +-- modelscope/            modelscope api
|   |   +-- mirror/                http, s3 style, and directory mirrors for air-gapped sites
|   |   +-- local/                 adopt files already on disk
|   +-- mirror/                    index files an export writes and a mirror source reads
|   +-- formats/                   classifier, groups, and header readers
|   |   +-- gguf/                  header and tensor table from range reads
|   |   |   +-- gguftest/          writes small GGUF files for tests
|   |   +-- safetensors/           shard headers plus config.json
|   +-- descriptor/                format-neutral descriptor, tensor groups, arch params
|   +-- store/                     content-addressed blobs, manifests, stable link tree, gc
|   +-- transfer/                  resumable chunked downloads, verification, throttling
|   +-- archive/                   tar and zip extraction with traversal checks
|   +-- estimate/                  planner placing tensor groups and caches into memory pools
|   +-- runtime/                   manifest model, param schema, command rendering
|   +-- build/                     recipe engine, fetch, patch apply, hashed build cache
|   |   +-- sandbox/               build runners, host toolchain or oci cli
|   +-- launch/                    process launcher, output files, adoption by pid, pdeathsig
|   +-- triage/                    log pattern matcher producing hints and fixes
+-- spec/                          every runtime and model specific lives here, never in go
|   +-- embed.go                   go:embed of the directories below
|   +-- runtimes/                  one manifest per backend, llamacpp.yaml, vllm.yaml
|   +-- formats/                   format descriptors, roles, file patterns
|   +-- archs/                     architecture families, cache shapes, attention variants
|   +-- recipes/                   build recipes as templates over host facts
|   +-- patches/                   unified diffs recipes reference by file
|   +-- probes/                    vendor tool invocations and output to fact mappings
|   +-- triage/                    failure patterns mapped to summaries and hints
+-- internal/
|   +-- daemon/                    wiring of all managers, startup recovery, shutdown
|   +-- inspect/                   resolve, describe, and plan a model before download
|   +-- pull/                      fetch, verify, link, manifest, and export a weight group
|   +-- installs/                  adopt, download, or build runtime binaries, run probes
|   +-- instances/                 plan, launch, supervise, persist, recover, and route
|   +-- calibrate/                 learned overhead corrections per runtime and arch
|   +-- doctor/                    probes, runtimes, installs, recipes, sources, and store as checks
|   +-- db/                        pure go sqlite, sql migrations, no proto in schema
|   |   +-- migrations/
|   +-- rpc/
|   |   +-- server.go              connect server, h2c, interceptors, auth, web ui mount
|   |   +-- services/              one file per proto service
|   +-- tasks/                     task engine, progress fan-out, cancellation, stored history
|   +-- slots/                     slot manager, reservations, swaps
|   +-- gateway/                   openai-compatible reverse proxy and the route table
|   +-- monitor/                   watches monitored models for new revisions and quants
|   +-- cli/                       client subcommands over connect, table and json output
+-- web/
|   +-- nebu/                      sveltekit static app embedded into the binary
|       +-- embed.go               go:embed of dist with a single page fallback
|       +-- src/lib/proto/         generated connect-es client, never hand edited
|       +-- src/lib/               api client, live state fed by events, shared components
|       +-- src/routes/            overview, catalog, store, runtimes, slots, instances, tasks, monitor, gateway, host, settings
|       +-- static/                openapi output
+-- test/
|   +-- fixtures/
|       +-- gguf/                  real headers with tensor data truncated
|       +-- safetensors/           shard headers and config.json samples
|       +-- probes/                captured vendor tool outputs across vendors
|       +-- logs/                  captured runtime logs for triage tests
+-- docs/                          user docs
+-- scripts/                       release and ci helpers
+-- .github/workflows/             ci with cgo guard, gen check, spec validation, tests
```

## Flows

**Inspect** is the pre-download fit table and the first milestone.

1. A source resolves a repo and revision to artifacts with sizes and sha256.
2. A format reader range-reads only headers and emits a descriptor.
3. The estimator places tensor groups and caches into the host's memory pools once per
   runtime manifest and candidate param set.
4. The result is a table of quant by runtime by context length, each marked fits, partial,
   or no, with the plan that produced it.

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

**Runtime install** adopts a binary you already have, downloads a prebuilt release, or runs a
recipe. A recipe is hashed with the resolved host facts, so an unchanged recipe on an
unchanged host is a cache hit.

**Build** runs as a task and produces an install of kind BUILT.

1. The host profile selects a variant, the first whose `when` holds and whose tools are on
   PATH, unless the request names one. Vars render in key order against the host env,
   the variant, and the ref, and request vars override them.
2. The ref resolves. `latest` reads the release feed for the newest tag. The recipe, variant,
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

**Run** is where the runtime becomes the ground truth.

1. The stored manifest supplies the link paths and descriptor. The host is probed again and
   the planner runs against free memory, so a second model plans around the first.
2. Solved params replace `auto`, the runtime package renders the command and environment
   from the manifest templates, and a free loopback port is picked.
3. The process launcher starts it in its own process group with its output appended to a
   file under the data dir, follows that file into a ring, and polls the manifest's health
   check until it answers. On Linux the child also gets a parent-death signal.
4. The route table maps the public model name to the instance endpoint and the gateway
   proxies by that name. Routes are rows in the store.
5. Report rules parse the runtime's own allocation lines into measurements, and the device
   free-memory delta feeds the calibration table, which shifts the estimator's overhead
   term for that runtime and architecture on the next plan.
6. On failure or unexpected exit, triage matches the output against the pattern catalog
   and the hint, with any suggested params, travels back on the task and the instance
   record.
7. Every state change writes the instance row, so `ps --all`, `show`, and `logs` answer for
   instances that ended before the daemon last started.

**Recovery** runs before the daemon listens. Tasks left unfinished are marked failed. For each
instance row that was not terminal, the daemon checks whether its pid is alive with the
recorded command line. A live runtime is adopted: its output file is followed from where it
is, it is routed as soon as it answers health, it is supervised by pid, and a stop signals
its group and escalates after the grace period. A dead one is marked stopped with the reason.
Every record still wanted, meaning it was never stopped by request and is not already live,
is relaunched from its original request one at a time, so each plans around the last. A stop
by request clears that intent; a daemon shutdown does not, which is why models survive a
reboot but not a `nebu stop`. Builds left running are marked failed. Slots pick up the live
instance bound to them and route it as soon as it answers health, and route rows come back
pending until an instance is adopted or relaunched.

**Installs** are adopted from a path or PATH, or downloaded through a prebuilt rule whose
`when` expression selects the release for the probed host. Manifest probes run the binary
once to capture its version and the devices it sees.

**Slots** reserve devices and a memory budget under one public name. A run bound to a slot
plans against the slot's device pools capped at the budget, inherits the slot's default
runtime and params, and can pin the process to the slot's devices through the launch env
templates. The slot's name is a route that lives as long as the slot: while nothing serves
it the gateway answers 503 with a retry hint rather than 404.

**Swap** is run against an occupied slot. The new model is planned with the old one still
running. When it fits, the new instance starts beside the old one, the route flips once
health passes, and the old instance drains, taking no new requests while in flight ones
finish, then stops. When it does not fit, or the caller asks, the old instance drains and
stops first, the route goes pending, and the new instance starts, with the old request
replayed as a rollback if the new one fails. The public name never disappears.

**Monitor** checks watched repositories on an interval, bypassing the listing cache, and
records a finding for a changed commit or a weight group that appeared or vanished. With
auto pull, groups matching the watch's pattern are pulled, and with a slot the slot swaps
onto the freshest pull. Findings stay until acknowledged.

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
- One file per service under `internal/rpc/services`.
- A bearer token on the API when `auth.token` is set, checked by one interceptor for unary
  and streaming calls. Gateway keys are separate under `gateway.api_keys`.

## Deliberate departures

- No protogorm. Proto stops at the API boundary. The store is SQL.
- No Docker SDK. Containers are one launcher among three and are driven through the CLI so
  podman and nerdctl work unchanged.
- No websocket hub. Live UI updates ride a Connect server stream, which works in browsers
  over HTTP/1.1 without a proxy.
- No cgo SQLite driver. The modernc pure-Go port. Installs, builds, instances with their
  plans, measurements, triage, and requests, slots, routes, watches, findings, calibrations,
  and task history are rows in `<data_dir>/nebu.db`, created by the embedded migrations.
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
  metadata keys onto descriptor params with optional derive expressions.
- `archs/` is `ArchSpec`. A regex over the architecture name and formulas such as
  `cache_per_token` evaluated with the descriptor params in scope.
- `runtimes/` is `RuntimeManifest`. Accepted formats, host constraints as expressions,
  acquisition including the recipe id, launch templates, typed params with their flags, the
  estimate policy, and report rules for calibration.
- `recipes/` is `Recipe`. Source as release feed, ref, archive, or repo templates, patches
  with conditions, variants selected by host expressions with their tools and vars, the
  sandbox, steps as templated argv, outputs, and the binary.
- `patches/` holds unified diffs a recipe patch names through `file`.

Expressions use expr syntax with `KiB`, `MiB`, `GiB`, `vercmp()`, and `num()` in scope.
Templates use Go text/template with `missingkey=error`, so optional fields go through
`index`, plus string helpers such as `join`, `replace`, `trimPrefix`, and `default`. Launch
templates also see `.devices`, the slot's devices with their facts, empty without a slot.

## Planner

The estimate policy is the only place a runtime's memory behaviour is described. Each
tensor group kind maps to a pool and, when offloadable, to the param that counts it. The
planner puts fixed kinds where the policy says, then searches offload counts in spill
priority order, most protected kind outermost, taking layers from the end first. A kind
that `requires` another can only sit on device where its parent does. Cache bytes follow
the layer kind, overhead sits on device, and the solved counts come back as params ready
to render as flags. The verdict is FITS when every offloadable item is on device, PARTIAL
when some spilled, and NO when the fixed need alone does not fit.

## Milestones

1. `nebu doctor` and `nebu inspect`. Host probes, sources, format readers, estimator.
   Ships as a CLI with no daemon state beyond a cache. Done.
2. `nebu pull`. Store and transfer with resume and verification. Done. Task history lives in
   the daemon, so `--detach` and `tasks` need `nebu serve` running.
3. `nebu run`. Installs, the process launcher, gateway, triage, calibration, the SQL store,
   recovery with adoption and relaunch. Done. Instances live in the daemon, so `run`, `ps`,
   `show`, `stop`, and `logs` need `nebu serve`, which the CLI finds on the configured
   listen address without flags.
4. `nebu build`. Recipes over host facts, patch sets, host and OCI sandboxes, the hashed
   build cache, and builds as rows next to installs. Done. Verified with a llama.cpp
   release built from its tarball on the host.
5. Slots, swaps, monitor, and the web UI on top of the same API. Done. Also the route
   table, gateway keys and API token, the event stream, ModelScope and mirror sources,
   store export, and the SvelteKit app embedded in the binary.
