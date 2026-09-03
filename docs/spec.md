# Spec files

Everything runtime or model specific lives under `spec/` as YAML that decodes through
protojson into one message from `proto/nebu/v1`. Go knows how to exec, parse, render, and
plan. Which tool, which flag, which fact, and which release is data.

| directory | message | purpose |
| --- | --- | --- |
| `probes/` | `ProbeSpec` | vendor tools and files read into devices, pools, and facts |
| `formats/` | `FormatSpec` | file roles, weight groups, tensor kinds, descriptor params |
| `archs/` | `ArchSpec` | architecture families and their cache formulas |
| `runtimes/` | `RuntimeManifest` | how to acquire, launch, probe, estimate for, and triage a backend |
| `triage/` | `TriageSpec` | log patterns mapped to summaries, hints, and param fixes |
| `recipes/` | `Recipe` | how to build a runtime from source, see recipes.md |
| `patches/` | unified diffs | files a recipe patch references through `file:` |

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
`.install` (`path`, `dir`, `version`), and `.devices`. `.devices` is empty unless the run
is bound to a slot with explicit devices, so an env template such as

```yaml
env:
  CUDA_VISIBLE_DEVICES: '{{range $i, $d := .devices}}{{if $i}},{{end}}{{index $d.facts "index"}}{{end}}'
```

pins the process to the slot's devices and renders empty, and is dropped, otherwise.
