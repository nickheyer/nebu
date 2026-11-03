# Tasks

Long running work with progress and logs.

Route: `/tasks`, `/tasks/<id>` opens the panel.

## Default view

```
 Active
   pull  Qwen/Qwen2.5-72B-Instruct       ▮▮▮▮▮▮▮▯▯▯▯  61 %    4m 12s   [Cancel]
   build vllm cu124                      ▮▮▮▮▮▮▮▮▮▮▮  —       9m 02s   [Cancel]

 History                     [Kind ▾] [State ▾]
   ✓  launch  llama-70b          42 s     2h ago    [Open]
   ✗  launch  qwen-72b           1m 10s   3h ago    [Open]
```

**Active.** Rows: kind pill, title (40ch), progress bar with percent or indeterminate, elapsed, Cancel. Tooltip on the bar: the message and bytes. Expand: one thin bar per progress row with its key.

**History.** Filters from the daemon. Rows: state mark, kind pill, title (40ch), duration, finished time, Open. Tooltip on a failed mark: the error.

## Panel

Header: title, kind, state pill, Cancel while active. Rows: created, started, finished, duration, labels. The progress block. The log: follow while running, tail after, with filter Text, copy, download. A link to the resource the task made when a label names one.

## Toasts

A task start raises a toast with its progress following the task. Success replaces it and fades. Failure stays until dismissed.

## Widths

Ultrawide: Split. Standard: drawer. Mobile: elapsed and duration move into the expand, the bar spans the row.

## Data

`ListTasks` with kind, state, cursor. `GetTask`, `WatchTask`, `CancelTask`. Stream: TASK.
