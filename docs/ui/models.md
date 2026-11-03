# Models

Downloaded weights and what fits where.

Route: `/models`, `/models/<source>/<repo>/<group>` opens the panel.

## Default view

```
 412 GB of 1 TB  ▓▓▓▓▓▓▓▓░░░░░░░░░░░░                            [Collect]

 ⌕ Search             [Kind ▾] [Runtime ▾] [Format ▾]          [Last used ▾]

 ●  meta-llama/Llama-3.3-70B-Instruct      Q4_K_M    42 GB    [Launch] ···
 ○  Qwen/Qwen2.5-72B-Instruct-AWQ           awq      38 GB    [Launch] ···
 ○  black-forest-labs/FLUX.1-dev            bf16     24 GB    [Launch] ···
```

**Store bar.** Used over limit, warning colour past it. Tooltip: models, partials, caches with bytes. Collect confirms with a partials switch and toasts the result.

**Filters.** Search and three Selects query the daemon. Sort: last used, pulled, size, name.

**Rows.** Filled dot when an instance runs it. Repository (40ch), precision label, size, Launch, menu: Inspect, Verify, Export, Remove. Secondary phrase on standard and up: parameter count, kind, compatible runtimes as up to three chips and +n. Tooltip on the precision label: format and quality level.

## Panel

Tabs: About, Files, Fit, Parts.

- **About.** Architecture, family, parameters, bits per weight, kind, generates. One stacked bar of tensor groups by kind. Store path with copy. Pulled, used, commit. Metadata under a collapse.
- **Files.** Rows: role icon, path (40ch), size. Tooltip: format, shard, digest. Download icon when a url exists. Warnings under a collapse.
- **Fit.** Runtime rows, one pill per configured context: fits, partial, no. Tooltip: weights and cache bytes. Switch: against free memory. Clicking a pill opens the plan popover: pool bars, placements, overhead, detail. Launch here.
- **Parts.** Rows: slot label, component, required mark, state pill (stored, bundled, missing). Pull parts when any required part is missing.

## Actions

Verify and Export start tasks. Export asks for a directory with Path. Remove confirms with a collect switch, refused while an instance runs the group.

## Widths

Ultrawide: Split with the panel open. Standard: drawer. Mobile: secondary phrase and size move into the expand, filters behind one Filters button.

## Data

`ListModels` with query, kind, runtime, format, sort, cursor. `GetModel`, `Inspect`, `Parts`, `Verify`, `Export`, `RemoveModel`, `Gc`, `ListRuntimes` once. Stream: MODEL, STORE, INSTANCE.
