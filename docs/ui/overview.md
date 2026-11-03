# Overview

What this machine is doing now, and what needs a person.

Route: `/`

## Default view

```
┌───────────┬───────────┬───────────┬───────────┬───────────┐
│ Serving   │ Requests  │ Attention │ Tasks     │ Store     │
│ 3 ready   │ 42 /min   │ 2         │ 1 active  │ 41 %      │
└───────────┴───────────┴───────────┴───────────┴───────────┘

Attention
 ●  qwen-72b failed          CUDA out of memory           [Open]
 ●  cert expires in 6 days   mesh home                    [Open]

Memory
 GPU0  H100 80G   ▓▓▓▓▓▓▓▓▓▓▓▓▓░░░░░▒▒▒░░   68 / 80 GB
 GPU1  H100 80G   ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓░░   72 / 80 GB
 CPU   512G       ▓░░░░░░░░░░░░░░░░░░░░░    6 / 512 GB

Serving
 ●  llama-70b     Llama-3.3-70B Q4_K_M       3 in flight   [Open]
 ●  embed-sm      nomic-embed-text           0 in flight   [Open]
 ◐  qwen-72b      starting                                 [Open]
```

** Tiles Amendment ** Tiles are bad and lazy UI/UX, do not do this. Do better, Think of something more innovative on the spot that satisfies the rest of the design constraints and screen widths. inline tiles/cards are generally not good.
<!-- **Tiles.** Five equal tiles in a Row. Each is a link. Tooltip on each: Serving lists pending and failed counts, Requests gives error rate and TTFT p50, Store gives bytes used and the limit. -->

**Attention.** Rows: dot, title (name, 24ch), reason (64ch), Open. Sources: false conditions on any resource, failed instances with their first triage hint, pools with no ready endpoint, store over its limit, certificate inside thirty days, failed tool servers, failed or degraded bots, tasks failed in the last hour. Empty: All clear. At most eight rows, then Show all.

**Memory.** One bar per device. Segment names and bytes in the tooltip. You will need to update a little .go code so that memory is not represented twice, for example we should not show an integrated gpu in cpu with system ram. Do not hardcode or string check anything look at the api, there is a option to do this the right and intended way generically.

**Serving.** Rows: state dot, route name, model (40ch), in flight, Open. Ready first. Eight rows, then Show all.

## First run

With no install, no model and no instance the page shows three steps in a Stack, each one line with its state and one button: Install a runtime, Pull a model, Launch.

## Widths

Ultrawide: Split, Attention and Serving left, Memory right. Standard: Stack as drawn. Mobile: tiles wrap two per row, rows drop in flight into the expand.

## Data

Live stream for every kind listed. `GetGatewayStatus` once for the Requests tile baseline.
