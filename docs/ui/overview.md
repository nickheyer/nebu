# Overview

What this machine is doing now, and what needs a person.

Route: `/`

## Default view

```
 Overview (i)

┌──────────────────────────────────────────────────────────────────────────────┐
│ Runtimes        ─▶ Models        ─▶ Instances      ─▶ Routes       ─▶ Traffic │
│ 2 installed        5 stored         3 running         3 ready         42 /min │
│ llama.cpp, vLLM    412 GB on disk   1 failed          1 pending       ▁▂▃▅▃▂  │
└──────────────────────────────────────────────────────────────────────────────┘
┌ Attention ─────────────────────────────────┐ ┌ Memory ───────────────────────┐
│ ●  qwen-72b      CUDA out of memory        │ │ GPU0  H100   ▓▓▓▓▓▓░░  68 / 80 │
│ ●  cert          expires in 6 days         │ │ CPU   512G   ▓░░░░░░░   6 / 512│
└────────────────────────────────────────────┘ └───────────────────────────────┘
┌ Serving ───────────────────────────────────┐ ┌ Recent tasks ─────────────────┐
│ ●  llama-70b   Llama-3.3-70B Q4_K_M      3 │ │ ✓ pull  Qwen2.5-72B    2m ago │
│ ●  embed-sm    nomic-embed-text          0 │ │ ✗ run   qwen-72b       9m ago │
└────────────────────────────────────────────┘ └───────────────────────────────┘
```

**Pipeline.** The path from a runtime to traffic, one stage each for Runtimes, Models, Instances, Routes and Traffic, each a link to its page with its count, a lamp and one detail line. Traffic carries the request sparkline. The line between two stages is a dashed wire that lights solid in the accent once the first holds anything. The first empty stage offers its next step as the one accent button, later empty stages as a quiet link, so a fresh host reads as three steps from empty to serving.

**Attention.** Shown while anything needs a person, absent otherwise. Rows: dot, title (name, 24ch), reason (64ch), opening the resource on click. Sources: false conditions on any resource, failed instances with their first triage hint, pools with no ready endpoint, store over its limit, certificate inside thirty days, failed tool servers, failed or degraded bots, tasks failed in the last hour. At most eight rows, then Show all.

**Memory.** One bar per pool, the CPU named on RAM and each GPU on its own. Segment names and bytes in the tooltip.

**Serving.** Rows: state dot, route name, model (40ch), in flight, opening the route on click. Ready first. Eight rows, then Show all. Empty: Nothing serving with Launch.

**Recent tasks.** The six newest tasks: mark, kind pill, title, age, opening the task on click.

## Widths

Wide: the pipeline across, then two columns, Attention and Serving left, Memory and Recent tasks right. Under 1000px one column. The pipeline wraps two stages per row under 860px and one under 520px.

## Data

Live stream for every kind listed. `ListTraces` over the last five minutes and `ListRuntimes` once on load.
