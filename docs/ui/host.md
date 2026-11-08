# Host

Devices, memory, storage, probes, the daemon, its tasks and its log.

Tabs: Overview `/host`, Tasks `/tasks` (see [tasks.md](tasks.md)), Logs `/host/logs`.

## Overview

```
 Host (i)
 Overview │ Tasks │ Logs

┌ Devices (i) ─────────────────────────────────────┐ ┌ Probes ─ Show all [Refresh] [Doctor] ┐
│ GPU0  NVIDIA H100 80GB   ▓▓▓▓▓▓▓▓▓▓░░  68 / 80 GB │ │ ✗  criu      not found              │
│ RAM   EPYC 9654          ▓░░░░░░░░░░░   6 / 512 GB │ │ ✓  5 probes passed                  │
└──────────────────────────────────────────────────┘ └─────────────────────────────────────┘
┌ Storage ─────────────────────────────────────────┐ ┌ Daemon ─────────────────────┐
│ /var/lib/nebu  ext4  ▓▓▓▓▓░░░░  412 GB / 1 TB  store │ host      lab-01          │
└──────────────────────────────────────────────────┘ │ system    linux amd64     │
                                                     │ probed    4m ago          │
                                                     │ version   nebu 0.9.3      │
                                                     └─────────────────────────────┘
```

**Devices.** One row per memory pool, the device drawing on it named, the CPU on the RAM row. Tooltip: segments by owner with bytes. Expand: each device's facts, its memory as its own or the pool it draws on, and the processes holding memory as rows with pid, owner, bytes. Refresh and Doctor sit in the Probes card. Doctor opens its log in a card over the others until it ends.

**Storage.** Rows: path (40ch), filesystem, bar, used over total, uses.

**Probes.** Rows: mark, id, detail (64ch). Failed first, the passing ones folded into one row until Show all.

**Daemon.** Host, system, probe time, version, listener, transport, auth, metrics and home with copy, then the host facts as rows.

## Logs

`/host/logs`, the log filling the viewport. Tail Select (200, 1000, 5000), follow Switch, filter Text, copy, download. Lines coloured by level prefix.

## Directory browser

Shared dialog behind every Path field: breadcrumb, Up, rows with folder or file icon, name (40ch), size, a typed path Text, a filter for directories or executables set by the caller. Pages from `ListDirectory`.

## Widths

Wide: two columns, Devices and Storage left, Probes and Daemon right. Under 1000px one column. Mobile: bars stack under their names, used over total under the bar.

## Data

`GetProfile`, `GetGatewayStatus` for the daemon card, `Doctor`, `Logs`, `ListDirectory`. Stream: HOST, INSTANCE, SLOT, STORE.
