# Tasks

Long running work with progress and logs.

The Tasks tab of Host. Route: `/tasks`, `/tasks/<id>` opens the panel.

## Default view

```
 Host  2 running · 1 failed in the last hour
 Overview │ Tasks │ Logs

┌ Running 2 ──────────────────────────────────────────────────────────────┐
│ pull   Qwen/Qwen2.5-72B-Instruct   ▮▮▮▮▮▮▮▯▯▯▯  61 %    4m 12s  Cancel │
└─────────────────────────────────────────────────────────────────────────┘
┌ History ─────────────────────────────────────────── [Kind ▾] [State ▾] ┐
│ ✓ launch  llama-70b                            42 s     2h ago           │
│ ✓ probe   Probe host ×15                        3 s     5h ago        ▾  │
└─────────────────────────────────────────────────────────────────────────┘
```

**Header.** Running, queued and failed in the last hour counts.

**Running.** Shown while anything runs. Rows: kind pill, title (40ch), progress bar with percent or indeterminate, elapsed, Cancel. Tooltip on the bar: the message and the amount, in bytes or steps as the progress counts. Expand: one thin bar per progress row with its key.

**History.** Filters from the daemon. Rows: state mark, kind pill, title (40ch), duration, finished time, opening the task on click. Runs of the same task with the same result back to back fold into the newest, carrying the run count, the rest in its expand. Tooltip on a failed mark: the error.

## Panel

Header: title, kind, state pill, Cancel while active. Rows: created, started, finished, duration, labels. The progress block. The log: follow while running, tail after, with filter Text, copy, download. A link to the resource the task made when a label names one.

## Toasts

A task start raises a toast with its progress following the task. Success replaces it and fades. Failure stays until dismissed.

## Widths

The panel sits beside the list once the page is 1080px wide, else in a drawer. Mobile: elapsed and duration move into the expand, the bar spans the row.

## Data

`ListTasks` with kind, state, cursor. `GetTask`, `WatchTask`, `CancelTask`. Stream: TASK.
