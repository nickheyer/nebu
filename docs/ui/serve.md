# Serve

Slots, the instances in them, instances without a slot, formations, and the launch wizard.

Route: `/serve`, `/serve/<instance>` opens the panel, `/serve/launch` the wizard.

## Default view

```
 Memory                                                [+ Slot]  [▶ Launch]
 GPU0  H100 80G   ▓▓▓▓▓▓▓▓▓▓▓▓▓░░░░░▒▒▒░░   68 / 80 GB
 GPU1  H100 80G   ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓░░   72 / 80 GB
 CPU   512G       ▓░░░░░░░░░░░░░░░░░░░░░    6 / 512 GB

 Slots
 ⋮ ●  llama-70b     Llama-3.3-70B Q4_K_M     3 in flight   [Swap]     ···
 ⋮ ○  slot-b        empty                                  [Launch]   ···
 ⋮ ●  qwen-72b      CUDA out of memory                     [Relaunch] ···

 Instances without a slot
   ●  embed-sm      nomic-embed-text          0 in flight   [Stop]     ···

 Formations
   ●  deepseek-v3   lockstep · 3 nodes                      [Open]     ···
```

**Slots.** Slot and Launch stay in the Memory header whatever the list holds. Drag handle, state dot, name (24ch), occupant model or error (40ch), in flight, one action by state, menu: Edit, Evict, Logs, Open instance, Delete. Tooltip on the name: runtime, devices, reserved memory, aliases. Expand: policy fields set, uptime, context. A swapping slot shows a thin canary bar under the row with the new share and time left.

**Instances without a slot.** Same row without the handle. Stopped and failed ones from the last day stay until dismissed from the menu.

**Formations.** Name, shape and node count, Open to Mesh. Hidden outside a mesh.

## Launch wizard

`NeoDialogStepper`, six steps, URL keeps the step. Starting from a slot, a model or a fit cell skips what is known.

1. **Model.** Picker over `ListModels`. Diffusion models list their parts with state pills.
2. **Runtime.** Segmented over compatible runtimes, install Select with the newest first and false conditions marked.
3. **Parameters.** One section per `Param.group`, advanced under its collapse. Each solved param: Auto Switch with the rule in a tooltip, the control under it when off.
4. **Placement.** Memory bars with the plan drawn on the chosen devices, verdict pill, warning line. In a mesh: span, shape, profile and a candidates list.
5. **Route.** Name, policy, tools, idle unload, snapshot, slot.
6. **Review.** The request as label and value rows, the command under a collapse, Run. The dialog follows the task to READY or FAILED, failure shows triage hints and Back.

## Fields

| Field | Component | Source |
| --- | --- | --- |
| model | Picker | `ListModels` |
| runtime | Segmented or Select | compatible runtimes, Select over four |
| install | Select | `ListInstalls` by runtime |
| param STRING | Text, Select when choices | `Param` |
| param INT, FLOAT | Slider when min and max, else Number, unit shown | `Param`, bounds refreshed from `ParamState` |
| param BOOL | Switch | |
| param PATH | Path, directory mode when `directory` | |
| param with `picks` | Select | stored parts matching the architecture |
| args | Text | 512 |
| placement | Segmented | device first, device only, host only |
| against free memory | Switch | |
| span | Checklist | nodes and devices from `ListNodes` |
| shape | Select | auto plus the seven shapes, one line each in option tooltips |
| plan profile | Segmented | chat, agent, batch, auto |
| name | Text | 64, default from the slot or repository |
| policy max_in_flight, burst | Number | 0 inherits |
| policy requests_per_second | Number | 0 inherits |
| policy request_timeout, upstream_timeout | Duration | up to one day |
| tool_servers | Checklist | `ListToolServers` mesh wide |
| mutating_tools | Switch | |
| idle_unload_seconds | Duration | 0 keeps loaded |
| snapshot | Switch | shown when the host probe found criu |
| slot | Select | this slot, new slot, none |

## Slot editor

A panel with: name Text 64, description Text 140, devices Checklist from the host profile, memory Bytes against the chosen devices' total, runtime Select, default params as in step three, policy as above, placement Segmented, aliases as rows of name Text with policy and tools under a collapse, args Text, position Number. Delete with a force Switch when occupied.

## Swap

Steps one to four with the slot fixed, then: drain first Switch, canary percent Percent 0 to 99, step seconds Duration up to one day, step percent Percent 0 to 99, max regression Percent 0 to 100, each with an `(i)`.

## Instance panel

Header: name, state pill, uptime, Stop, Logs. Tabs: Overview, Logs, Adapters, Ranks, Seat, Triage.

- **Overview.** Rows: model, runtime and install, endpoint with copy, pid, restarts, health with last probe time, context, quantization, accelerator, modalities, template source and tool support. Planned against measured as one bar per pool. Conditions. Params and command under collapses.
- **Logs.** Follow Switch, tail Select (200, 1000, 5000), filter Text, copy, download.
- **Adapters.** Rows: name, path (40ch), Unload. Load form: name Text 64, path Path.
- **Ranks.** Rows: rank, endpoint, health dot. Tooltip: pid, port. Hidden with one rank.
- **Seat.** Rows: formation, role and rank, layers, transport, rendezvous. Peers as rows: node, role, address. Shown with a seat.
- **Triage.** Rows: summary, hint. Tooltip: matched line. First tab when failed.

## Widths

Ultrawide: Split, lists left, panel right. Standard: drawer, wizard as a dialog at 720px. Mobile: bars stack, occupant and in flight move into the expand, wizard full screen.

## Data

`Run`, `StopInstance`, `Logs`, `LoadAdapter`, `UnloadAdapter`, `ListAdapters`, `CreateSlot`, `UpdateSlot`, `DeleteSlot`, `Swap`, `EvictSlot`, `RelaunchSlot`, `GetSlot`, `Estimate`, `Parts`, `PlanFormation`, `RunFormation`, `ListModels`, `ListInstalls`, `ListToolServers`, `ListNodes`, `ListDirectory`. Stream: INSTANCE, SLOT, FORMATION, HOST, ROUTE, TASK.

## Questions

- Ad hoc launches: create a slot by default, or only when asked?
- Canary progress is read from the swap task's progress rows unless the swap exposes it elsewhere.
