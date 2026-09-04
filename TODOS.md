# TODOs

Ordered by the sequence they should be worked. Each item says what is missing, why it matters,
and where to look.

Rules for whoever works this list:

- Do not move to next step until your current step is complete.
- Do not introduce gaps, partial code, or placeholders.
- Do not write non unit tests, and the unit tests you do write must be as minimal as possible and provide maximum coverage.
- NEVER EVER EVER CREATE DEPENDENCIES OR COUPLING WHERE THERE DOESNT NEED TO BE.
- ONE SIZE FITS ALL, MAKE IT WORK. 
- Consolidate code. The less code needed for the same feature, user experience, and readability is always the better option.
- Do NOT write any comments that occupy 2 or more consecutive lines, or are longer than 10-15 words, or contain em dashes/semicolons/non-ascii chars. 

> The most important rule of all that defies everything else on this page: Never opt for the easier implementation, always opt for the complete implementation with zero fail cases. It is never excusable to "fail loudly", you can never fail at all ever. Your implementation should be generic enough that it literally conforms to anything without writing a single coupling implementation or one off. One-offs are inexcusable. Anytime you have a question for "Nick", just ask yourself instead "What are the options available?" then choose the most difficult and complete option with no consideration for your own interests. If anything in the below todos is a lazy answer for a tough problem, you have full permission and are required to implement the tough solution for the tough problem instead, zero excuses. If you followed these instructions, there should be no more questions. 

DO NOT WRITE TESTS JUST TO WRITE TESTS, NO MATTER WHAT THE BELOW SAYS. YOU SHOULD BE JUSTIFYING EVERY SINGLE LINE OF CODE AND ALWAYS BE LOOKING FOR EXISTING CODE THAT CAN BE CONSOLIDATED OR REMOVED ENTIRELY, REGARDLESS OF WHAT YOU ARE WORKING ON OR HOW LONG THAT CODE HAS BEEN THERE. ZERO EXCUSES. 

## Verify against the real world

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

14. **Runtime knobs are profiles now.** Done. A profile is a named param set for one runtime,
    rows in the database like sources, never a spec file, one per runtime marked default. Params
    layer in one order everywhere a plan is made, in `instances.prepare` and in the inspector's
    estimate and fit table: the default or named profile, then the slot's defaults, then the
    request's own. A named profile picks its runtime when nothing else does, and a watch names the
    profile its swaps start from. `nebu profiles` lists, adds, updates, and removes them by id or
    name, `--profile` rides on `run`, `swap`, and `monitor add`. The web run, slot, and watch
    dialogs share `ParamForm`, a typed form built from `manifest.params` with types, choices,
    descriptions, and inherited values as placeholders, and the runtimes page edits profiles
    through the same form. Manifests validate every profile write. `nebu inspect --profile` and `--slot`
    plan the same way, the slot's runtime standing in when the request names none, the inspector
    applies the same calibration delta a run does, a watch or want resolves its profile when
    added and keeps the id, a swap keeps the request as prepared so the slot holds the profile by
    id, and a profile anything a restart would relaunch names is removed only with `--force`,
    which clears the references. `nebu runtimes show` prints the params a profile may name.

15. **Four runtimes.** Done. llama.cpp, vLLM, SGLang, and NeMo each ship as a manifest, a
    recipe, and triage rules under `spec/`, and no Go file names any of them. SGLang was the
    test: a Python module launched from its venv interpreter, `-m sglang.launch_server`, with a
    probe that imports the package, and the schema needed nothing new. vLLM gained the slot device
    pinning env the llama.cpp manifest already had. NeMo and SGLang still need a host with an
    NVIDIA GPU to run, item 21.

16. **The web client keeps no tables.** Done. `FormatSpec` carries `blurb` and `precisions`,
    rules that read a group's width, label, and notes from its name or a metadata key, and the
    new `spec/precisions/` table puts each bit width into words. The descriptor builder writes
    the result as `Descriptor.precision`, so the fit table and the drawer show what the daemon
    said. `RuntimeService.ListFormats` serves the format specs and the client reads them once per
    connection. The Hub's housekeeping tags sit on its `Catalog` as `Noise` and reach the client
    as `SourceCapabilities.hidden_tags`. Source and provider names were already daemon data.

## Gateway and operations

17. **Limiters and policy.** Done as three changes. Gateway: `Policy` on `gateway.policy` and
    on every slot, in flight cap, token bucket rate and burst, request timeout, and upstream
    timeout, each zero field of a slot inheriting the gateway's, enforced in `Table.Acquire` and
    the proxy, answering `429` and `504`, with the upstream timeout defaulting to ten minutes so a
    hung runtime never holds a drain. Store: `store.max_bytes` with `Store.Evict`, which removes
    the models unused longest before a pull, sparing what runs and what slots relaunch, and
    `used_at` touched by every run. Transfer: `transfer.windows`, spans of the week with their own
    rate or a pause, followed live by one retuned token bucket in `transfer.Schedule`. Eviction and gc
    wait for pulls in flight and drop only the evicted model's orphaned blobs, the model being
    pulled is spared, planning does not touch `used_at`, and recipe fetches and CLI or LFS
    downloads follow the limits and paused windows too. Pulls in flight reserve their bytes
    against the cap, a launch holds its model's key so an eviction waits and then spares it, a
    pause that begins mid chunk closes the connection without spending a retry, the HTTP client
    bounds only the steps a server can hang on, git clones and git served files wait for the
    schedule, a Hugging Face source moves chunk by chunk under any limit rather than through its
    CLI, the store announces its totals on the stream, and removal and verification take the
    same locks a pull does.

18. **Three flavors, TLS, and CORS.** Done. `ApiFlavor` names OpenAI, Anthropic, and Ollama,
    each one `Flavor` in `internal/gateway` that reads and writes requests, answers, streams, and
    errors over one canonical chat, so the gateway serves any client format from any runtime
    format and passes matching ones through untouched. Ollama's tags, ps, show, and version and
    Anthropic's model list answer from the route table. Every gateway path answers CORS
    preflights, narrowed by `gateway.cors_origins`. `tls.cert_file` and `tls.key_file` put the
    API, web UI, and gateway behind TLS with HTTP/2, and a listener beyond loopback without TLS,
    a token, or keys is warned about at start. The CLI dials https and trusts the configured
    certificate, preflights allow whatever headers the SDK asks, `/v1/models/NAME`, the Ollama
    heartbeat at `/`, `count_tokens` through the runtime's `/tokenize` or an estimate, and 413
    on oversized bodies answer too, and a slot's limit edits reach its live route at once. A route
    carries the name the runtime serves, sent upstream in place of the route name so aliases and
    slots reach vLLM and SGLang, a stream that breaks off ends with an error in the caller's
    shape, Ollama tool results are tied to their calls and its images typed by their bytes, URL
    images are fetched for it, counters reach the stream once a second, and the keys warning
    covers a gateway sharing an exposed API listener.

19. **Every operating system.** Done. `pkg/proc` is the one place processes are started, found,
    and stopped: a group with a parent-death signal on Linux, a group elsewhere on Unix, and on
    Windows a console process group inside a job object that dies with the daemon, killed as a
    tree, found again by pid and command line, and interrupted with a console break. The launcher
    and the build sandbox both use it. Windows storage reads the volume, and Windows and macOS
    hosts get memory and CPU probes, so planning has a host pool everywhere. The tree compiles for
    linux, windows, darwin, and freebsd. Windows and macOS probe GPUs of any vendor, macOS counts
    reclaimable pages as free, and the prepare step runs under `pkg/proc` too. Apple silicon's
    memory is one unified pool, probes select by `arch` beside `os`, AMD and Windows cards report
    free bytes and an `index` fact every launch template pins by, the daemon on Windows claims a
    console its children share and a job they inherit and breaks into a foreign console through a
    copy of itself, darwin and FreeBSD read their mounts from statfs, the data dir is the
    platform's own, and the stub for platforms that cannot exec is gone.

## NeMo

21. **NeMo has not run.** Its wheels need Python 3.12 and this host has 3.14 only, so the recipe
    could not be built here; manifest, recipe, prepare step, triage, and estimate policy compile
    and the fit table plans NGC checkpoints, nothing more. **Needs a host with Python 3.12 and an
    NVIDIA GPU** to build the recipe, serve a NeMo 2 directory, convert a packed `.nemo`, and read
    the logs. The converter accepts only the base models it lists, so `model_id` matters.

## Catalog and tests

20. **Every source at once, wanted models, and notifications.** Done. `Registry.Search` fans a
    request with no source and no kind out to every source, the same code the provider merge
    used, and the catalog has an All tab, `nebu search` doing the same by default. A `Want` is a
    standing search kept as a row: query, provider or source, format, group regex, and the pull
    and swap settings a watch has; the monitor runs it on the interval, resolves the first hits,
    and the first matching weight group satisfies it with a `wanted_found` finding, a pull, and a
    swap. Findings now name their source and belong to a watch or a want, and carry the swap
    their pull set off. Every finding reaches the web UI as a toast wherever you are, desktop
    notifications are a settings toggle, and `notify.webhooks` posts each finding and each
    failed instance as JSON through a queue with retries. Findings list by want id, a satisfied
    want looks again with `--rearm`, one check owns each watch and want it touches, a want's
    format, runtime, and kind are checked when added, the fan out drops per provider sorts and
    facets, a pruned finding leaves the stream, and the monitor and notifier live as long as the
    daemon. Removing a slot or a source that watches and wants name is refused until forced.

## Interactive

22. **Prompts.** Done. The web chat page and `nebu chat` both talk to a ready route through the
    gateway itself, streaming, with a system prompt, temperature, and a token cap, showing tokens
    and tokens per second per answer. A running instance's drawer opens the chat on it. `nebu chat` checks the
    route is ready first, and a slot instance's drawer opens the chat on the slot name.

## DB

23. **One migration, written by atlas.** Done. `internal/db/schema.sql` is the schema, `atlas.hcl`
    points atlas at it, and `make migrate-reset` writes the single init migration and its
    `atlas.sum` from it, the rule until release, with `migrate-diff`, `migrate-hash`,
    `migrate-validate`, and `migrate-status` beside it through the same docker image discopanel
    uses. The daemon applies the embedded directory with atlas's executor and revision table, checks
    the directory against `atlas.sum` first, logs any drift between the live schema and
    `schema.sql`, and sets a database from the hand rolled runner aside as a dated copy instead of
    failing on it. A database whose revision the rewritten directory no longer holds is
    diffed against `schema.sql`, brought to the head in place with its rows kept, and baselined,
    behind a dated copy of the file and as one transaction, drift naming the column or index.
    The instance and slot requests keep `force`, plans keep the correction they carried, routes
    keep the served name, findings and wants keep the swap they set off. 