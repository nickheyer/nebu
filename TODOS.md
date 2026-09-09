# TODO LIST

Ordered by implementation order. They are to be completed top to bottom and each item removed from list when completed. Based on ./SCREENS/*.png analysis.

1. Create `SizeBar.svelte`: height fits text plus padding; props `total: number`, `overlays: { start: 'left'|'right', items: { label?: string, size: number }[] }[]`; overlays layer (e.g. diff drawn red with label); markers per overlay, default one marker row per overlay; replaces every `Meter`/`StackBar` use (`routes/host`, `routes/store`, `Devices`, `PlanView`, `ModelDrawer`, `RunDialog`).
2. Create `FactTable.svelte`: key/value of any length or type; long strings wrap or truncate with expand, numbers right-aligned, nested values indented; replaces `Kv` for host facts and device facts.
3. Numeric params with a manifest range render as a slider (min/max/step from manifest) instead of `NumberInput` steppers (`ParamForm.svelte:116`).
4. `ago()` formats past 30 days in the largest whole unit (weeks/months/years) instead of "647d ago" (`format.ts:160`).
5. Remove `Default: ` and `Slot default: ` prefixes from all select items/placeholders/labels (`ConfigForm.svelte:50`, `ParamForm.svelte:64`, `RunDialog.svelte:185`).
6. Remove inline explanatory text: "Inference servers nebu can install…" (`routes/runtimes/+page.svelte:18`), "against memory free at launch" and "as the plan solved them" (`routes/instances/[id]/+page.svelte:152,155`), "Use this name in client requests." (`RunDialog.svelte:195`), "256k is the model's maximum" (`ModelDrawer.svelte:379`), "Fits in memory" (`RunDialog.svelte:220`), "Runtime defaults" and "Empty fields use runtime defaults." (`RunDialog.svelte:247,251`), "default limits first byte 600 s" (`Connect.svelte:64`).
7. File-tree rows get a hover vertical-ellipsis menu toggled by visibility (no layout shift) with Download file and Copy path (`ModelFiles.svelte`).
8. All table column headers sort asc/desc, client-side where the source has no server sort: `ModelFiles.svelte` (Name, Role, Weights, Size), `routes/catalog/+page.svelte:304`.
9. GGUF group key must include tokens preceding the quant: `…-MAX-IQ4_XS.gguf`, `…-MAX-LOW-MTP-IQ4_XS.gguf`, `…-MAX-MTP-IQ4_XS.gguf` currently collapse into one `IQ4_XS` group summing to 16.6+15.3+17.0=48.9 GB (shown 50 GB; Q6_K→71, Q8_0→61); one file per group unless shard suffix matches (`spec/formats/gguf.yaml:18-21`).
10. GGUF draft rule only matches names starting with `mtp`/`draft`, so `…-MTP-IQ4_XS.gguf` is classified as weights; match the token anywhere in the name (`spec/formats/gguf.yaml:9`).
11. HF hardcodes revision `main` and ModelScope `master`; resolve the default branch from the source API as csghub/github do (`pkg/sources/huggingface.go:48`, `modelscope.go:43`, pattern at `csghub.go:235`, `github.go:216`).
12. Ollama revisions come from scraping HTML with regex; use the registry (`/v2/library/<name>/tags/list`, per-tag manifest config blob `file_type`/`model_type`) and never parse size or quant from the tag string (`pkg/sources/ollama.go:181-235`).
13. Ollama catalog rows: fill FORMAT (always gguf), SIZE, LIKES, TASK from the source; hide any column the source cannot fill instead of rendering dashes (`pkg/sources/ollama.go:120-160`, `routes/catalog/+page.svelte`).
14. Ollama drawer: one tag per size×quant confirmed via #12 → remove the tag dropdown and render every tag as a variant row grouped by parameter count (`ModelDrawer.svelte:318-333`).
15. Rewrite runtime manifest descriptions as what the runtime is, not launch mechanics ("llama-server from ggml-org llama.cpp", "sglang.launch_server over safetensors checkpoints, a Python module run from its virtual environment", etc.) (`spec/runtimes/*.yaml:3`).
16. Context length default: largest context that fits the memory plan, capped at the model's trained context, instead of static 8192 (`spec/runtimes/llamacpp.yaml:106`, `ModelDrawer.svelte:94-96` currently picks the smallest planned, `RunDialog.svelte`); params table shows the rule, not a number.
17. KV cache type default derives from weights precision and memory plan instead of `f16` always; offer only types the runtime accepts with the chosen quant and flash attention state (`spec/runtimes/llamacpp.yaml:164,172`, `ParamForm.svelte`).
18. Shared listener reports its own address from the daemon; `baseUrl = window.location.origin` shows the Vite port `5173` in dev (`api.ts:34`, `Connect.svelte:19-20`).
19. Memory estimate plans against free memory at dialog open with no timestamp and no accounting for other nebu instances (`free: true`, `RunDialog.svelte:82`); show device capacity with overlays other instances / this plan / free and re-estimate on an interval (`PlanView.svelte`).
20. Host Facts: `Kv` → `FactTable` (`routes/host/+page.svelte:95`).
21. Host Devices: drop the `vendor · kind · pool · id` subtitle string; facts inside the disclosure → `FactTable`, disclosure stays (`Devices.svelte:45-57,99`).
22. Host facts drop what devices already show (`cpu.flags`, `cpu.model`, `cpu.count`, `cpu.threads`, `nvidia.count`, `nvidia.driver_version`, `mem.total`); keep `os`, `arch`, hugepages.
23. Store disk bar plots store bytes over disk total ignoring other usage ("0 B of 981 GB" with 172.8 GB free); remove bar and size text, then `SizeBar` with total=disk, overlays other usage / store / free (`routes/store/+page.svelte:80,204-207`).
24. Catalog: remove the "60 of 239" row; "N results" inline with the filter row, right-aligned opposite filters (`routes/catalog/+page.svelte:396`).
25. Catalog: sort select `w-40` truncates "Most downloaded"; size select to its longest label and shrink the search input (`routes/catalog/+page.svelte:375`).
26. Weights table: name the "NOT LOADED" column by what it is (tensors the runtime skips, here the MTP prediction head, the "draft ×2 1.2 GiB" row) or move it to a tooltip on the total (`PlanTable.svelte:105`).
27. Weights variant order is fit-rank then bytes (reads 4,5,5,4,4,4,3,2,6,8-bit); order by bits per weight descending for all rows and mark the recommended row in place (`catalog.ts:137-144`).
28. Runtimes list: per row name, status, one action; drop `reads gguf · api openai` chips (`routes/runtimes/+page.svelte`).
29. Runtime detail header: remove status and description from the title line (`routes/runtimes/[id]/+page.svelte`).
30. Merge "Install" and "Installs" tabs into one Installs tab with an Add action that opens the method form (`routes/runtimes/[id]/+page.svelte:26-31`).
31. "llama-server is not on PATH" renders as method row detail and again as a banner; keep the row detail, drop the banner (`routes/runtimes/[id]/+page.svelte:161,166-170`).
32. Install submit button text is the method label ("Use a binary on this host"); use "Install" (`routes/runtimes/[id]/+page.svelte:207`).
33. Install submit is enabled while required Binary is empty; disable until required fields validate (`routes/runtimes/[id]/+page.svelte:207`, `ConfigForm.svelte`).
34. Install Settings card sizes to field count: one field sits inline with the method choice, not a half-width input in a page-wide card (`routes/runtimes/[id]/+page.svelte:200-203`).
35. "Method" card title duplicates the `Choices` label "Method"; keep one (`routes/runtimes/[id]/+page.svelte:157-162`).
36. Recipe card variant table and Settings variant select both pick the variant; pick in one place (`routes/runtimes/[id]/+page.svelte:172-198`).
37. Parameters tab: "advanced" is rendered inside the parameter name; make it a column or row group (`routes/runtimes/[id]/+page.svelte` params tab, `ParamForm.svelte`).
38. Manifest tab: replace the two raw key/value cards (`{{.install.path}}`, `len(devices) > 0`) with annotated YAML plus copy, or fold Launch and Host requirements into an overview and delete the tab (`routes/runtimes/[id]/+page.svelte`).
39. Serve Endpoints: one origin line with three dialect suffixes and one copy each beside the curl tabs, instead of the same origin three times plus the curl panel (`Connect.svelte:34-60`).
40. Serve "Model names"/"Add alias": rename to Aliases and hide until an instance exists (`Routes.svelte:68-70`).
41. Serve: add a slots table (name, model, runtime, state) with the runtime-install empty state inside it so "New slot" has a table, or move the button to `/slots` (`routes/+page.svelte:39,56`).
42. Instance: delete the Memory plan tab; plan bars move into Overview, "Parameters as the plan solved them" card is dropped (`routes/instances/[id]/+page.svelte:28,152-155`).
43. Instance header prints name, status, then the model reference equal to the name; show the reference only when it differs (`routes/instances/[id]/+page.svelte:77-91`).
