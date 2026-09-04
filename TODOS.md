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

## Configuration and sources

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

11. **The estimator has never been calibrated on hardware.** Close the calibration loop against a
    real card and keep a fixture of plan versus measured `n_swa` is
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

15. **Two runtimes.** which backend is next. Adding it is a manifest plus a
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
    on the gateway listener. Anthropic AND Ollama compatible flavor is
    in scope.

19. **Windows is unsupported without saying so.** Adoption, process groups, and parent death
    signals are stubbed on non unix, they should not be. nebu is built for every operating system.
    end of story. 

## NeMo

21. **NeMo has not run.** Its wheels need Python 3.12 and this host has 3.14 only, so the recipe
    could not be built here; manifest, recipe, prepare step, triage, and estimate policy compile
    and the fit table plans NGC checkpoints, nothing more. **Needs a host with Python 3.12 and an
    NVIDIA GPU** to build the recipe, serve a NeMo 2 directory, convert a packed `.nemo`, and read
    the logs. The converter accepts only the base models it lists, so `model_id` matters.

## Catalog and tests

20. **One provider at a time.** After item 7 the catalog merges sources within a provider. This
    item is search across providers, a wanted list, and notifications beyond the monitor page.

## Interactive

21. **Prompts** Should easily be able to build a somewhat primitive way to prompt loaded models through a chat ui or other medium, in a "debug" session sort of way. From ui and cli if possible.

## DB

22. Flatten the migrations into 1, install atlas like ~/code/discopanel, do that and there should only ever be 1 migration until we release. 