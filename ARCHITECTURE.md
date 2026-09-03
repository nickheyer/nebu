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
7. Launchers are plural. A bare process is the default. systemd units and OCI containers are
   alternatives, not the design.

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
│           ├── host.proto         host profile, devices, memory pools, facts
│           ├── source.proto       sources, search, resolve
│           ├── model.proto        model, revision, artifact, format, role, descriptor
│           ├── store.proto        blob store state, pulls, verification, gc
│           ├── runtime.proto      runtime manifest, install, params, constraints
│           ├── recipe.proto       recipes, patch sets, build results
│           ├── estimate.proto     memory plan requests and results
│           ├── slot.proto         slots, instances, lifecycle, swap
│           ├── gateway.proto      routes, listeners, api flavors
│           ├── task.proto         durable jobs, progress and log streams
│           └── event.proto        watch stream feeding live ui updates
├── pkg/
│   ├── proto/nebu/v1/             generated go and connect-go, never hand edited
│   ├── config/                    daemon config, paths and listeners only
│   ├── logger/                    structured logging with rotation
│   ├── events/                    in-process bus behind the watch stream
│   ├── spec/                      loads embedded spec files into proto messages, validates
│   ├── host/                      host profile assembly and fact evaluation
│   │   └── probes/                generic exec, csv, json, kv, sysfs readers driven by spec
│   ├── sources/                   source interface, search, resolve, range reads
│   │   ├── huggingface/           hub api, tree with sha256, download urls
│   │   ├── modelscope/            modelscope api
│   │   ├── mirror/                http and s3-style mirrors for air-gapped sites
│   │   └── local/                 adopt files already on disk
│   ├── formats/                   header readers into a common descriptor
│   │   ├── gguf/                  header and tensor table, range-read capable
│   │   └── safetensors/           shard headers plus config.json
│   ├── descriptor/                format-neutral descriptor, tensor groups, arch params
│   ├── store/                     content-addressed blobs, manifests, stable link tree, gc
│   ├── transfer/                  resumable parallel downloads, verification, throttling
│   ├── estimate/                  planner placing tensor groups and caches into memory pools
│   ├── runtime/                   manifest model, param schema, command rendering, installs
│   ├── build/                     recipe engine, fetch, patch apply, hashed build cache
│   │   └── sandbox/               build sandboxes, host toolchain or oci cli
│   ├── launch/                    launcher interface, supervision, health, pdeathsig
│   │   ├── process/               bare process launcher, default
│   │   ├── systemd/               transient unit launcher via systemd-run
│   │   └── oci/                   container launcher via docker, podman, or nerdctl cli
│   └── triage/                    log pattern matcher producing hints and fixes
├── spec/                          every runtime and model specific lives here, never in go
│   ├── runtimes/                  one manifest per backend, llamacpp.yaml, vllm.yaml
│   ├── formats/                   format descriptors, roles, file patterns
│   ├── archs/                     architecture families, cache shapes, attention variants
│   ├── recipes/                   build recipes as templates over host facts
│   ├── probes/                    vendor tool invocations and output to fact mappings
│   └── triage/                    error patterns mapped to hints and actions
├── internal/
│   ├── daemon/                    wiring of all managers, startup recovery, shutdown
│   ├── db/                        pure go sqlite, sql migrations, no proto in schema
│   │   └── migrations/
│   ├── rpc/
│   │   ├── server.go              connect server, h2c, interceptors, auth
│   │   └── services/              one file per proto service
│   ├── tasks/                     task engine, durable jobs, cancellation, log capture
│   ├── slots/                     slot manager, reservations, instance lifecycle, swaps
│   ├── gateway/                   openai-compatible reverse proxy routed by model name
│   ├── calibrate/                 records measured allocations to refine estimates
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

**Install** has two halves that share the task engine.

- Model install pulls artifacts through transfer into the store, verifies against source
  sha256, writes a manifest, and links a stable human path that never changes.
- Runtime install adopts a binary you already have, downloads a prebuilt release, or runs a
  recipe. A recipe is hashed with the resolved host facts, so an unchanged recipe on an
  unchanged host is a cache hit.

**Run** is where the runtime becomes the ground truth.

1. The slot manager reserves devices and a budget.
2. The runtime package renders a command and environment from the manifest and params.
3. A launcher starts it, holds it under supervision, and polls the manifest's health check.
4. The gateway adds a route for the model name.
5. Calibrate parses the runtime's reported allocations into the estimator's correction table.
6. On failure, triage matches the log against the pattern catalog and returns a hint and,
   when safe, a retry with adjusted params.

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

## Milestones

1. `nebu doctor` and `nebu inspect`. Host probes, sources, format readers, estimator.
   Ships as a CLI with no daemon state beyond a cache.
2. `nebu pull`. Store and transfer with resume and verification.
3. `nebu run`. Runtime manifests, the process launcher, gateway, triage, calibration.
4. `nebu build`. Recipes and sandboxes.
5. Slots, swaps, monitor, and the web UI on top of the same API.
