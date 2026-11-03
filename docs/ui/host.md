# Host

Devices, memory, storage, probes, the daemon log.

Route: `/host`, `/host/logs`

## Default view

```
 lab-01    linux amd64    probed 4m ago   (i)            [Refresh]  [Doctor]

 Devices
   GPU0  NVIDIA H100 80GB      ▓▓▓▓▓▓▓▓▓▓▓▓▓░░░░░▒▒▒░░   68 / 80 GB   ···
   GPU1  NVIDIA H100 80GB      ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓░░   72 / 80 GB   ···
   CPU   AMD EPYC 9654         ▓░░░░░░░░░░░░░░░░░░░░░    6 / 512 GB   ···

 Storage
   /var/lib/nebu    ext4     ▓▓▓▓▓▓▓░░░░░░░░░░░░░░   412 GB / 1 TB    store
   /tmp             tmpfs    ▓░░░░░░░░░░░░░░░░░░░░     2 GB / 64 GB   cache

 Probes
   ✓  nvidia-smi     550.54
   ✗  criu           not found
```

**Header.** Hostname, os and arch, probe time. `(i)` popover: home directory, facts count. Refresh runs the probe again. Doctor starts the task and opens its log under the header until it ends.

**Devices.** Memory bars. Tooltip: segments by owner with bytes. Menu: expand facts, which lists the device facts and the processes holding memory as rows with pid, owner, bytes.

**Storage.** Rows: path (40ch), filesystem, bar, used over total, uses.

**Probes.** Rows: mark, id, detail (64ch). Failed first. Skipped under a collapse.

**Facts.** A collapse at the bottom with the host facts as rows.

## Logs

Tab or `/host/logs`. Tail Select (200, 1000, 5000), follow Switch, filter Text, copy, download. Lines coloured by level prefix.

## Directory browser

Shared dialog behind every Path field: breadcrumb, Up, rows with folder or file icon, name (40ch), size, a typed path Text, a filter for directories or executables set by the caller. Pages from `ListDirectory`.

## Widths

Ultrawide: Split, devices left, storage and probes right. Standard: Stack. Mobile: bars stack under their names, used over total under the bar.

## Data

`GetProfile`, `Doctor`, `Logs`, `ListDirectory`. Stream: HOST, INSTANCE, SLOT, STORE.
