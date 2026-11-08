# Traffic

Requests through the gateway, live and past, and the story of any one.

Route: `/traffic?route=&window=`, `/traffic/<trace>` opens the panel.

## Default view

```
 [All routes ▾] [Kind ▾] [Status ▾]  [Live 5m ▾]  ⏸         List │ Timeline

 Requests 42 /min ▁▂▃▅   Errors 0.4 % ▁▁▂▁   TTFT 180 ms ▃▂▂▃   TPOT 24 ms ▂▂▂▂

 12:01:03   chat    llama-70b    200     1.2 s     [Open]
 12:01:02   embed   embed-sm     200     40 ms     [Open]
 12:01:01   chat    chat         429     shed      [Open]
```

**Filters.** Route Picker, kind Select, status class Select, window Select (live 1m, 5m, 15m, or a range), pause. More filters in a popover: node, objective, fairness id.

**Facts.** A flat strip of four facts sharing the row, requests per minute, error rate, TTFT, TPOT, each with a sparkline over the window. Tooltips: p95 values, attainment per band, queue wait p95.

**List.** Rows: time, kind, route (24ch), status, duration or the shed reason, Open. Tooltip on the status: error text. Tooltip on the duration: TTFT, prompt and completion tokens, cached tokens. Small marks after the route for forwarded, translated, failed open, fallbacks. Newest first, pages from the daemon.

**Timeline.** A toggle at standard width and up. One lane per endpoint, the window fitted to the width, bars from start to finish coloured amber until first token, green after, red on error. Hover tooltip: route, status, tokens, timings. Click opens the panel. Idle lanes collapse to a line.

## Panel

Tabs: Summary, Routing, Tools, Bodies.

- **Summary.** A strip with started, first byte, first token, finished and the durations between. Rows: request id with copy, route, pool, endpoint, kind, client and upstream api, stream, status, stop, tokens, cached, queued behind, objective and attained, priority, owner, remote, node and entry node, seats. Error line when any.
- **Routing.** Profile and score. Filters as rows with the dropped count, tooltip listing the endpoints. Candidates as rows: endpoint, score, a small bar per scorer in a tooltip. Fallbacks as rows: address, reason, written mark.
- **Tools.** Rows: name, server or client, duration, mark. Expand: arguments, result.
- **Bodies.** Request, upstream request when different, response, each as Code json read only with copy and a capped mark.

## Widths

Ultrawide: Split, list left, panel right, timeline above the list. Standard: drawer, timeline toggle. Mobile: facts two per row, list only, kind moves into the expand.

## Data

`ListTraces` with filters, cursor, limit. `GetTrace`. `ListRoutes` for the Picker. Stream: TRACE, POOL.
