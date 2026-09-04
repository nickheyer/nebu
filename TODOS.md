# TODOs

Ordered by the sequence they should be worked. Each item says what is missing, why it matters,
and where to look.

Rules for whoever works this list:

- Nick decides. Where an item says **Nick decides**, do not pick an option; ask, then do what he
  says. Everywhere else the item names one approach and that is the one to take.
- A test whose server and client are both ours proves nothing. Fixtures are recorded from the real
  service and checked in verbatim; end to end tests drive the real daemon. Never hand write a
  response body.
- Providers and transports are Go behind one interface each. Sources are rows. There are no source
  spec files. See ARCHITECTURE.md, Sources.
- Go stays generic. Anything that names a vendor, model family, runtime flag, or quant scheme goes
  in `spec/` or in one localized provider module, never in shared code and never in the web client.
- "Docs describe the target" means the documentation already states the contract and the code has
  to be brought to it.
- Terms: a **provider** is a platform module, a **source** is a configured instance of one, a
  **transport** moves bytes. "Catalog" means the web page. `Catalog` in backticks means the Go
  struct in `pkg/sources/client.go` that holds a provider's facts.

## Make the tree green

1. **Transfer tests do not compile.** `pkg/transfer/transfer_test.go` calls `sources.NewClient`,
   which the sources refactor renamed to `sources.NewHTTP`, so `go test ./...`, `go vet ./...`,
   and CI fail. The file stands up a fake range server and drives our fetcher against it, which is
   the kind of test Nick rejected. Delete the file unless Nick says to keep it. If he keeps it,
   the only change is the rename at lines 72, 97, and 128.

2. **Done: docs and code disagreed on source names and endpoints.** Docker Hub registers as
   `dockerhub`, the docs said `oci`. Ollama's API host is the website, the docs said the registry.
   ARCHITECTURE.md drew deleted packages. Fixed in this pass. Nothing to do unless a provider moves
   again.

## Record reality first

3. **Eight remote providers, zero recorded reality.** Probes, GGUF, safetensors, and triage test
   against output captured from real tools under `test/fixtures`; the provider modules have
   nothing. Before touching them for items 4 to 7, capture one real response for each provider's
   search, resolve, revisions, card, and one ranged read, check them in verbatim under
   `test/fixtures/sources/<provider>/`, and replay them. Add a smoke test gated by an environment
   variable that hits the live endpoints. Civitai, Kaggle, and NGC captures need Nick's tokens.
   The Ollama module scrapes HTML by Tailwind class names and will break silently, which is why
   this comes before the refactor.

## Configuration and sources

4. **Sources become rows.** Add a `sources` table and migration, a source service with create,
   update, and delete, and load the registry from the database, rebuilding it when a row changes.
   Seeded defaults are rows too: on start, create one source per provider that runs unconfigured,
   under the provider's name, when no row has that name. The config `sources` list is the same
   operation from a file: create when the name is absent, otherwise ignore. Today `sources.Build`
   rejects config for every built in kind, so no hub can take a token env or an endpoint such as
   hf-mirror.com, GHCR, a self hosted CSGHub, or an NGC proxy; `newClient` already honors
   `endpoint` and `token_env`, so the guard goes and the sources test asserting it goes with it.
   The row's name is today's `id` field. Docs describe the target. Seeded defaults cant' be deleted.

5. **Provider config schemas.** Each provider declares the fields a source of it accepts, per
   transport it uses: endpoint, credential, namespace, path, each with a type and whether it is
   required. The declaration is a generic field list in `SourceCapabilities`; the values are a
   `map<string,string>` on `Source` that the provider validates on create and update. That
   replaces the `options` map nothing reads today. The UI renders the form from the declaration
   and never knows a provider by name.

6. **Transports behind one interface.** `HTTP`, `Distribution`, and the file blob in
   `pkg/sources` are concrete types provider modules reach for directly. Define one transport
   interface, put those three behind it, then add git with and without LFS and the Hugging Face
   CLI. A provider names the transports it uses. Then add GitHub as a provider on git plus the
   releases API, and route the two hand rolled GitHub release parsers through it: `latestTag` in
   `pkg/build/fetch.go` and `resolveAsset` in `internal/installs/installs.go`.

7. **Sources in the UI.** The settings page creates, edits, and deletes sources from the schema in
   item 5. The catalog page merges every source of a provider into one listing, marks which
   source each hit came from, and filters by source where the provider allows it. Depends on 4
   and 5.

## Verify against the real world

8. **No CUDA prebuilt rule for Linux.** `spec/runtimes/llamacpp.yaml` has ROCm, Vulkan, CPU,
   arm64, and macOS rules; an NVIDIA Linux host matches the Vulkan rule. The CUDA build is two
   archives, the binary and the cudart companion, and `PrebuiltRule` holds one asset pattern. Give
   the rule a list of assets in the proto, then add the CUDA rule above the Vulkan one with a
   vendor check in its `when`. Proto plus yaml, no vendor names in Go.

9. **Slot device pinning is not wired.** The slots and spec docs describe pinning through launch
   env templates; the shipped llama.cpp manifest has no `env` block. Add one, or render
   llama-server's `--device` flag, which also covers Vulkan and ROCm. Either way it is a change to
   the manifest yaml with zero Go. Until then a slot on a two GPU host confines the plan but not
   the process.

10. **vLLM has never run.** Manifest, recipe, triage, and estimate policy exist; only llama.cpp
    has been exercised. **Needs Nick's GPU host** to run it, measure, and fix the overhead term.
    Adoption bug: after a daemon restart the recorded command line is compared to
    `/proc/<pid>/cmdline`, and a venv shim rewrites argv, so a live vLLM would be marked dead and
    relaunched beside itself. Fix by recording the process start time from `/proc/<pid>/stat` in
    the instance row and matching pid plus start time. Do not match on the port.

11. **The estimator has never been calibrated on hardware.** Close the calibration loop against a
    real card and keep a fixture of plan versus measured; **needs Nick's GPU host**. `n_swa` is
    extracted by both formats and used nowhere, so sliding window models overshoot; the fix is a
    cache formula in `spec/archs/` that knows which layers use the window, no Go. Device pools are
    summed into one capacity, which matches llama.cpp's layer split but is wrong for vLLM at
    tensor parallel one; add a field to `EstimatePolicy` saying whether the runtime spans devices.
    A slot budget caps each device pool today; Whether it should cap each pool or the total across
    the slot's devices is whatever you can anecdotaly prove is the better option, or both if you
    cant.

12. **Catalog says fits, run says no.** Inspect plans against total memory, run plans against free
    memory. The fit table shows both verdicts, labelled, and the inspector takes a
    flag for free versus total.

13. **Civitai browses, NGC serves.** Nick decided. The diffusion work that served Civitai's
    checkpoints was removed; Civitai checkpoints classify as nothing and the catalog lists them
    with no weight groups. NGC's NeMo 2 directories classify as `nemo2`, read from
    `context/model.yaml` and the torch distributed `.metadata` pickle with Megatron's stacked
    layers split per layer, and run on NeMo Export-Deploy's Ray Serve script through
    `spec/runtimes/nemo.yaml` and a venv recipe. Packed `.nemo` archives classify as `nemo`,
    read from the tar's config beside a pickle, zarr, or distributed checkpoint, and run on the
    same runtime through its prepare step, which converts them once into the group's prepared
    directory with NeMo's converter from the recipe's second environment. Generic pieces this
    added: `FormatSpec.reader` and `root`, `ParamRule.tensor`, `Launch.prepare`,
    `CommandProbe.command`, `RunRequest.force`, the store's prepared tree, `.descriptor` in
    launch templates, launch args that render empty are dropped, the gateway follows same host
    redirects, the raw header cache is keyed by the format spec, a re-pull prunes links it no
    longer names, and a weight whose format requires files the repository lacks falls through to
    the next format that claims it. Kaggle needed nothing. What is left is item 21.

## Runtimes and parameters

14. **Runtime knobs are per run only.** Params carry type, choices, and description in the
    manifest; the run dialog is a free text box. Build a typed form from `manifest.params`. Add
    per runtime default profiles as rows in the database, the same pattern as sources, not as
    spec files: spec is seeded, profiles are user data. Let the UI edit them. README goal two
    asked for configuring providers with all their knobs.

15. **Two runtimes.** **Nick decides** which backend is next. Adding it is a manifest plus a
    recipe plus triage rules, and it is the test that the manifest schema is not shaped around
    llama.cpp.

16. **Domain knowledge leaked into the web client.** `web/nebu/src/lib/catalog.ts` hard codes
    quant name regexes, bit widths, K quant flavors, Hub housekeeping tags, per format blurbs, and
    source display names. That belongs in data the daemon serves: the precision table and blurb on
    `FormatSpec`, housekeeping tags and display name on the provider's `Catalog` struct exposed
    through `SourceCapabilities`. The client keeps no tables.

## Gateway and operations

17. **No limiters or policy.** The gateway counts requests but has no rate limit, concurrency cap,
    per route timeout, or upstream timeout, so a hung runtime hangs the client and the drain
    counter forever. The store has no size cap or eviction. Transfer has one global rate limit and
    no schedule. Each is its own change; do not bundle them.

18. **One API flavor, one transport.** `ApiFlavor` has one value. The listener is plain HTTP with
    one shared bearer token and no warning about binding beyond loopback. A separate gateway
    listener will hit CORS from the browser. Add TLS or document the reverse proxy, and add CORS
    on the gateway listener. **Nick decides** whether an Anthropic or Ollama compatible flavor is
    in scope.

19. **Windows is unsupported without saying so.** Adoption, process groups, and parent death
    signals are stubbed on non unix and the release script builds Linux and macOS only.
    **Nick decides:** state it in the README or build the stubs.

## NeMo

21. **NeMo has not run.** Its wheels need Python 3.12 and this host has 3.14 only, so the recipe
    could not be built here; manifest, recipe, prepare step, triage, and estimate policy compile
    and the fit table plans NGC checkpoints, nothing more. **Needs a host with Python 3.12 and an
    NVIDIA GPU** to build the recipe, serve a NeMo 2 directory, convert a packed `.nemo`, and read
    the logs. The converter accepts only the base models it lists, so `model_id` matters.

## Catalog and tests

20. **One provider at a time.** After item 7 the catalog merges sources within a provider. This
    item is search across providers, a wanted list, and notifications beyond the monitor page.
