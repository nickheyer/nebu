# Discover

Search every source, inspect a repository, pull.

Route: `/discover?source=&q=&sort=&...`, `/discover/<source>/<repo>`

## Search view

```
 [huggingface ▾]  ⌕ qwen 72b instruct               [Trending ▾]  [Filters 2]

 ⚠ token missing                                                   [Settings]

 Qwen/Qwen2.5-72B-Instruct                              145 GB   [Open]
 bartowski/Qwen2.5-72B-Instruct-GGUF                     41 GB   [Open]
 unsloth/Qwen2.5-72B-Instruct-bnb-4bit                   41 GB   [Open]

                              [ Load more ]
```

**Bar.** Source Select over configured sources, the last used one remembered. One source at a time, so every sort and facet is the selected source's own. Query Text. Sort Select from the chosen source's sorts with a direction toggle when reversible. Filters opens a popover of the source's facets as Selects or Chips, and author. The button shows the active count.

A warning line under the bar when the selected source reports an error or needs a token, linking to Settings.

**Rows.** Repository (40ch), size, or the parameter count with its unit (70B params), Open. Check mark when in the store. Secondary phrase on standard and up: kind, up to three formats, license. Tooltip: downloads, likes, updated, gated, private.

## Repository page

```
 ← Discover    Qwen/Qwen2.5-72B-Instruct (i)

 Card │ Files │ Fit │ Pull
```

- **Header.** Back link and the repository. The group Select, revision Select from `ListRevisions`, default first with size and precision in option tooltips, and the source's web link sit in the tools of the open tab's card, a Stored pill beside them when any group is in the store.
- **Card.** The model card html, sanitized.
- **Files.** Rows: role icon, path (40ch), size. Sharded weights as one brace pattern row expanding to a cell grid, as on Models. Warnings under a collapse.
- **Fit.** As on Models, for the selected group. Incompatible runtimes show the reason in a tooltip.
- **Pull.** Weights only Switch, Node Picker when in a mesh showing this node's name while empty, a Checklist of required parts not stored, each with its size, total bytes, Pull, Pull and launch.

## Fields

| Field | Component | Source |
| --- | --- | --- |
| source | Select | `ListSources` |
| sort | Select | source `sorts` |
| facets | Select or Chips per facet | source `facets`, Chips when `freeform` |
| author | Text | 64 |
| revision | Select | `ListRevisions` |
| group | Select | resolved groups |
| node | Picker | `ListNodes` |
| alone | Switch | |
| parts | Checklist | `Parts` missing entries |

## Widths

Ultrawide: repository page as a Split, card left, tabs right. Standard: tabs as drawn. Mobile: rows drop the secondary phrase into the expand, Filters as a sheet, the repository header on two rows, back and title then source and revision.

## Data

`ListSources`, `Search` with cursor, `ListRevisions`, `Resolve`, `GetModelCard`, `Inspect`, `Parts`, `Pull`. Stream: MODEL for store marks.
