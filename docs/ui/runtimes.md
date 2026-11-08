# Runtimes

The runtime catalog, installed copies, builds.

Route: `/runtimes`, `/runtimes/<id>` opens the panel, `/runtimes/<id>/install` with its install form open, `/runtimes/<id>/defaults` on Defaults.

## Default view

```
 ●  llama.cpp                  2 installs      [Install] ···
 ●  vLLM                       1 install       [Install] ···
 ○  SGLang                     needs cuda 12   [Open]
 ●  stable-diffusion.cpp       no install      [Install] ···
```

Rows: compatibility dot, name, install count or the first unmet reason (64ch), Install, menu: Open. Tooltip on the name: description, formats, api, model kind. Compatible first.

## Panel

Tabs: Installs, Builds, Defaults. An Install button beside the tabs toggles the install form over the installs list.

- **Installs.** Rows: version (12ch), kind pill, origin, Remove. Tooltip: path, directory, created. Expand: facts, conditions, slots and instances using it. Remove is refused with the daemon's message while referenced.
- **Install form.** Method as Segmented. Then one form per method, see Fields. Adopt answers at once. Prebuilt and Build start a task followed inline.
- **Builds.** Rows: state dot, variant, ref (12ch), duration, Remove. Tooltip: commit, sandbox, image, directory, binary. Expand: error, vars, facts, patches, task link.
- **Defaults.** The runtime's params as the launch form renders them, each box showing the runtime's own default. Values set here are stored in `Settings.runtime_defaults` and merged under every slot and launch on the runtime, which may override any of them.

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
| ref | Text | 64, a tag mark for the latest release while empty |
| rebuild | Switch | |

Absent tools disable Build and list them in a line.

## Widths

Ultrawide: Split. Standard: drawer. Mobile: the reason phrase moves into the expand.

## Data

`ListRuntimes`, `GetRuntime`, `ListFormats`, `AdoptInstall`, `Install`, `RemoveInstall`, `ListRecipes`, `Build`, `ListBuilds`, `RemoveBuild`, `ListDirectory`, `GetSettings`, `UpdateSettings`. Stream: INSTALL, BUILD, SLOT, INSTANCE, SETTINGS.
