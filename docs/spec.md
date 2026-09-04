# Spec files

Everything runtime or model specific lives under `spec/` as YAML that decodes through
protojson into one message from `proto/nebu/v1`. Go knows how to exec, parse, render, and
plan. Which tool, which flag, which fact, and which release is data.

| directory | message | purpose |
| --- | --- | --- |
| `probes/` | `ProbeSpec` | vendor tools and files read into devices, pools, and facts |
| `formats/` | `FormatSpec` | file roles, weight groups, tensor kinds, descriptor params |
| `archs/` | `ArchSpec` | architecture families and their cache formulas, sliding window families included |
| `runtimes/` | `RuntimeManifest` | how to acquire, launch, probe, estimate for, and triage a backend |
| `triage/` | `TriageSpec` | log patterns mapped to summaries, hints, and param fixes |
| `recipes/` | `Recipe` | how to build a runtime from source, see recipes.md |
| `precisions/` | `PrecisionSpec` | words for a weight group by its bits per weight |
| `patches/` | unified diffs | files a recipe patch references through `file:` |

## Formats and readers

A `FormatSpec` claims files by pattern and names the Go reader that parses the group through
`reader`, its own id when empty, so several formats can share one parser. Formats claim files in
priority order, and a weight whose format requires a file the repository lacks falls through to
the next format that claims it. `root` is a regex with a `root` group for formats laid out as a
tree, such as a NeMo 2 checkpoint whose config sits under `context/` and whose shards sit under
`weights/`: files sharing a root attach to the group instead of files sharing a directory, and
the store keeps the tree under the group. A param rule with `tensor` sums the elements of
matching tensors.

A format also carries the words the UI shows for it: `blurb` says what the format is, and
`precisions` reads a weight group's precision from its group name or a metadata key. A rule
matches `key`, the group name when it is `group`, with a regex whose `bits` capture or `bits`
field names the width and whose `name` capture labels it; the first matching rule with a width
claims it, and every matching rule adds its `note`, expanded with the captures. The width then
picks its words from the `precisions/` table, which lists labels, blurbs, and quality levels by
bits in descending order, the first width at or below the group's winning. A group whose name
and headers say nothing falls back to its measured bits per weight. The descriptor carries the
result as `precision`, so the web client keeps no quant tables of its own.

## Archs and the planner

An `ArchSpec` matches architecture names with a regex and declares formulas the planner
evaluates with the descriptor params, the run params, and the runtime's cache element sizes in
scope; formulas may reference one another in any order. `cache_per_token` is what the runtime's
`cache_bytes` multiplies by the context length. A sliding window family such as `gemma3` sets
`swa_pattern`, every nth layer keeping a full cache, taking the metadata value when the
checkpoint carries one, and folds `min(n_ctx, n_swa)` into the per token figure so those layers
stop growing with the context. A runtime's `estimate.devices_param` names the param counting
the devices a run spans, `tensor_parallel_size` for vLLM, so capacity is that many of the
largest pools; without it the runtime spreads over every device. `overhead_bytes` is fitted to
measured runs and the calibration table shifts it per runtime and architecture after each run.

`launch.prepare` names formats a runtime turns into something else before the first launch and
the command that does it, rendered like the launch. It runs once per stored group into the group's
prepared directory, which `.artifacts` carries as `prepared_dir` and which becomes `weights_dir`
for the launch. A probe may name a `command` template to run instead of the install.

A site adds or replaces any spec by dropping a file with the same `id` into a directory
listed in `spec_dirs`. `<data_dir>/spec` is always the last layer. Files are matched by id,
not by name, so the file name is free.

Expressions use expr syntax with `KiB`, `MiB`, `GiB`, `TiB`, `vercmp()`, and `num()` in scope.
Templates use Go text/template with `missingkey=error` plus `join`, `split`, `lower`, `upper`,
`trimPrefix`, `trimSuffix`, `replace`, `contains`, `hasPrefix`, `hasSuffix`, `default`, `quote`,
`list`, `first`, and `num`. Optional fields go through `index`.

## Launch templates

`launch.command`, `launch.args`, `launch.env`, and param defaults render with `.name`,
`.params`, `.artifacts` (role name to link path, plus `weights_dir`), `.host`, `.port`,
`.install` (`path`, `dir`, `version`), `.devices`, and `.descriptor` (`format_id`,
`architecture`, `params`, `metadata`, `total_bytes`, `parameter_count`). Optional
params go through `index` and `default`, so a param default such as

```yaml
params:
  - name: model_id
    default: '{{index .descriptor.metadata "tokenizer.type"}}'
```

reads the checkpoint's tokenizer id from the metadata its format reader collected, which is
how the NeMo manifest names the base model its converter builds a packed archive as. `.devices`
is empty unless the run is bound to a slot with explicit devices, so an env template such as

```yaml
env:
  CUDA_VISIBLE_DEVICES: '{{range $i, $d := .devices}}{{if $i}},{{end}}{{index $d.facts "index"}}{{end}}'
```

pins the process to the slot's devices and renders empty, and is dropped, otherwise.
