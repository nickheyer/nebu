# Runtimes

The runtime catalog, installed copies, builds.

Route: `/runtimes`, `/runtimes/<id>` opens the panel, `/runtimes/<id>/install` on its Install tab.

## Default view

```
 ●  llama.cpp                  2 installs      [Install] ···
 ●  vLLM                       1 install       [Install] ···
 ○  SGLang                     needs cuda 12   [Open]
 ●  stable-diffusion.cpp       no install      [Install] ···
```

Rows: compatibility dot, name, install count or the first unmet reason (64ch), Install, menu: Open. Tooltip on the name: description, formats, api, model kind. Compatible first.

## Panel

Tabs: Installs, Install, Builds, Params.

- **Installs.** Rows: version (12ch), kind pill, origin, Remove. Tooltip: path, directory, created. Expand: facts, conditions, slots and instances using it. Remove is refused with the daemon's message while referenced.
- **Install.** Method as Segmented. Then one form per method, see Fields. Adopt answers at once. Prebuilt and Build start a task followed inline.
- **Builds.** Rows: state dot, variant, ref (12ch), duration, Remove. Tooltip: commit, sandbox, image, directory, binary. Expand: error, vars, facts, patches, task link.
- **Params.** Read only reference grouped as the launch form renders it: label, type, default, unit, bounds, auto rule.

## Fields

| Field | Component | Source |
| --- | --- | --- |
| method | Segmented | `InstallOption` list |
| adopt path | Path | binaries searched on PATH as placeholder |
| prebuilt settings | per `ConfigField` type | string Text, url Text, path Path, env Secret, bool Switch, int Number, choices Select with disabled reasons |
| variant | Select | recipe variants, unmet ones disabled with reason |
| vars | per `Var` | Select when choices, else Text with default |
| sandbox | Segmented | host, oci |
| image | Text | shown for oci |
| ref | Text | 64 |
| force | Switch | |

Absent tools disable Build and list them in a line.

## Widths

Ultrawide: Split. Standard: drawer. Mobile: the reason phrase moves into the expand.

## Data

`ListRuntimes`, `GetRuntime`, `ListFormats`, `AdoptInstall`, `Install`, `RemoveInstall`, `ListRecipes`, `Build`, `ListBuilds`, `RemoveBuild`, `ListDirectory`. Stream: INSTALL, BUILD, SLOT, INSTANCE.
