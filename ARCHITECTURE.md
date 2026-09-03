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

## Tree

```
nebu/
├── ARCHITECTURE.md                this document
├── README.md                      intro and objectives
├── Makefile                       gen, run, build, test, lint, cgo guard
├── flake.nix                      dev shell with go, node, buf
├── buf.yaml                       proto module, lint and breaking rules
├── buf.gen.yaml                   go, connect-go, connect-es, openapi outputs
├── go.mod
├── cmd/
│   └── nebu/
│       └── main.go                single binary, dispatches serve and client subcommands
├── proto/
│   └── nebu/
│       └── v1/
│           ├── config.proto       daemon and client configuration
│           ├── host.proto         host profile, devices, memory pools, facts
│           ├── source.proto       sources, search, resolve
│           ├── model.proto        model, revision, artifact, format, role, descriptor
│           ├── store.proto        stored models, pulls, verification, gc
│           ├── runtime.proto      runtime manifest, installs, params, triage, constraints
│           ├── instance.proto     running models, run, stop, logs
│           ├── recipe.proto       recipes, patch sets, build results
│           ├── estimate.proto     memory plan requests and results
│           ├── slot.proto         slots, instances, lifecycle, swap
│           ├── gateway.proto      routes, listeners, api flavors
│           ├── task.proto         durable jobs, progress and log streams
│           └── event.proto        watch stream feeding live ui updates
├── pkg/
│   ├── proto/nebu/v1/             generated go and connect-go, never hand edited
│   ├── config/                    daemon config, paths and listeners only
│   ├── logger/                    structured logging
│   ├── eval/                      expression and template engines every spec file uses
│   ├── cache/                     disk cache for listings and header reads
│   ├── events/                    in-process bus behind the watch stream
│   ├── spec/                      loads layered spec files into proto messages
│   ├── host/                      host profile assembly and fact evaluation
│   │   └── probes/                generic exec, csv, json, kv, sysfs readers driven by spec
│   ├── sources/                   source interface, search, resolve, range reads
│   │   ├── huggingface/           hub api, tree with sha256, download urls
│   │   ├── modelscope/            modelscope api
│   │   ├── mirror/                http and s3-style mirrors for air-gapped sites
│   │   └── local/                 adopt files already on disk
│   ├── formats/                   classifier, groups, and header readers
│   │   ├── gguf/                  header and tensor table from range reads
│   │   │   └── gguftest/          writes small GGUF files for tests
│   │   └── safetensors/           shard headers plus config.json
│   ├── descriptor/                format-neutral descriptor, tensor groups, arch params
│   ├── store/                     content-addressed blobs, manifests, stable link tree, gc
│   ├── transfer/                  resumable chunked downloads, verification, throttling
│   ├── estimate/                  planner placing tensor groups and caches into memory pools
│   ├── runtime/                   manifest model, param schema, command rendering, installs
│   ├── build/                     recipe engine, fetch, patch apply, hashed build cache
│   │   └── sandbox/               build sandboxes, host toolchain or oci cli
│   ├── launch/                    process launcher, output ring, health polling, pdeathsig
│   └── triage/                    log pattern matcher producing hints and fixes
├── spec/                          every runtime and model specific lives here, never in go
│   ├── embed.go                   go:embed of the directories below
│   ├── runtimes/                  one manifest per backend, llamacpp.yaml, vllm.yaml
│   ├── formats/                   format descriptors, roles, file patterns
│   ├── archs/                     architecture families, cache shapes, attention variants
│   ├── recipes/                   build recipes as templates over host facts
│   ├── probes/                    vendor tool invocations and output to fact mappings
│   └── triage/                    failure patterns mapped to summaries and hints
├── internal/
│   ├── daemon/                    wiring of all managers, startup recovery, shutdown
│   ├── inspect/                   resolve, describe, and plan a model before download
│   ├── pull/                      fetch, verify, link, and manifest a weight group
│   ├── installs/                  adopt or download runtime binaries, run probes
│   ├── instances/                 plan, launch, supervise, and route running models
│   ├── calibrate/                 learned overhead corrections per runtime and arch
│   ├── doctor/                    probes, runtimes, installs, sources, and store as checks
│   ├── db/                        pure go sqlite, sql migrations, no proto in schema
│   │   └── migrations/
│   ├── rpc/
│   │   ├── server.go              connect server, h2c, interceptors, auth
│   │   └── services/              one file per proto service
│   ├── tasks/                     task engine, progress fan-out, cancellation, log capture
│   ├── slots/                     slot manager, reservations, swaps
│   ├── gateway/                   openai-compatible reverse proxy routed by model name
│   ├── monitor/                   watches monitored models for new revisions and quants
│   └── cli/                       client subcommands over connect, table and json output
├── web/
│   └── nebu/                      sveltekit static app embedded into the binary
│       ├── embed.go               go:embed of build output
│       ├── src/lib/proto/         generated connect-es client, never hand edited
│       ├── src/lib/               api client, stores, shared components
│       ├── src/routes/            catalog, store, runtimes, slots, tasks, host
│       └── static/                openapi output and assets
├── test/
│   └── fixtures/
│       ├── gguf/                  real headers with tensor data truncated
│       ├── safetensors/           shard headers and config.json samples
│       ├── probes/                captured vendor tool outputs across vendors
│       └── logs/                  captured runtime logs for triage tests
├── docs/                          user docs
├── scripts/                       release and ci helpers
└── .github/workflows/             ci with cgo guard, gen check, spec validation, tests
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

**Run** is where the runtime becomes the ground truth.

1. The stored manifest supplies the link paths and descriptor. The host is probed again and
   the planner runs against free memory, so a second model plans around the first.
2. Solved params replace `auto`, the runtime package renders the command and environment
   from the manifest templates, and a free loopback port is picked.
3. The process launcher starts it in its own process group tied to the daemon's lifetime,
   captures output into a ring, and polls the manifest's health check until it answers.
4. The gateway routes the public model name to the instance endpoint.
5. Report rules parse the runtime's own allocation lines into measurements, and the device
   free-memory delta feeds the calibration table, which shifts the estimator's overhead
   term for that runtime and architecture on the next plan.
6. On failure or unexpected exit, triage matches the output against the pattern catalog
   and the hint travels back on the task and the instance record.

**Installs** are adopted from a path or PATH, or downloaded through a prebuilt rule whose
`when` expression selects the release for the probed host. Manifest probes run the binary
once to capture its version and the devices it sees.

**Swap** is run against an occupied slot. The old instance drains, the new one starts, and the
route flips when health passes. The public name never disappears.

## Conventions carried over from discopanel

- `proto/**/*.proto` is the API. `make gen` regenerates Go, connect-go, connect-es, and the
  OpenAPI document. Generated code is never hand edited.
- Buf v2 with remote plugins, so no local protoc.
- SvelteKit with adapter-static, Tailwind, bits-ui, embedded via `go:embed`.
- Connect over h2c on one listener. The gateway shares that listener by default under `/v1/`
  and can be split to its own address in config.
- One file per service under `internal/rpc/services`.

## Deliberate departures

- No protogorm. Proto stops at the API boundary. The store is SQL.
- No Docker SDK. Containers are one launcher among three and are driven through the CLI so
  podman and nerdctl work unchanged.
- No websocket hub. Live UI updates ride a Connect server stream, which works in browsers
  over HTTP/1.1 without a proxy.
- No cgo SQLite driver. The modernc pure-Go port.
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
  acquisition, launch templates, typed params with their flags, the estimate policy, and
  report rules for calibration.

Expressions use expr syntax with `KiB`, `MiB`, `GiB`, `vercmp()`, and `num()` in scope.
Templates use Go text/template with `missingkey=error`, so optional fields go through
`index`.

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
3. `nebu run`. Installs, the process launcher, gateway, triage, calibration. Done. Instances
   live in the daemon, so `run`, `ps`, `stop`, and `logs` need `nebu serve`, which the CLI
   finds on the configured listen address without flags.
4. `nebu build`. Recipes and sandboxes.
5. Slots, swaps, monitor, and the web UI on top of the same API.
