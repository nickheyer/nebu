# Monitor

The monitor watches repositories for new revisions and weight groups, and searches for
models you want until they turn up.

```
nebu monitor add org/model --match 'Q4_K_M|Q5_K_M' --auto-pull --slot main
```

Adding a watch resolves the repository once and records the commit and the group names as
the baseline. Every `monitor.interval_ms`, and on `nebu monitor check`, each watch resolves
again, bypassing the listing cache, and records findings:

- `new_revision` when the commit changed
- `new_group` when a weight group appeared
- `removed_group` when one disappeared

With `--auto-pull`, groups matching `--match` are pulled when they appear, and groups that
are already stored are pulled again when the revision changes. With `--slot`, the slot is
swapped onto the freshest pull, preferring the group it already serves. Each finding carries
the pull it set off as its task and the swap that followed as its swap task, and everything is
visible on the monitor page of the web UI, where findings stay until acknowledged. A slot or a
runtime a watch names must exist when it is added. One check owns each watch and want it
touches, so a scheduled check and a check by hand never pull and swap the same one twice, and
a check of one that is being checked right now is refused.

## Wanted

```
nebu monitor want Llama 4 Scout --format gguf --match 'Q4_K_M' --slot main
nebu monitor wants
nebu monitor unwant ID
```

A want is a standing search. Every interval, and on `nebu monitor check`, the query runs
across every source, or one provider with `--kind`, or one source with `--source`, and the
first hits are resolved in the order the sources rank them. The first repository holding a
weight group of the wanted `--format` whose name matches `--match` satisfies it: a
`wanted_found` finding records where, and with `--auto-pull` or `--slot` the group is pulled
and the slot swapped onto it with the runtime, profile, and params given, the same way a
watch does. A satisfied want stops looking and keeps what it found. `nebu monitor check ID
--rearm`, or Look again on the monitor page, forgets it and looks once more, and `check ID`
without it is refused so a stray check cannot pull and swap twice. `nebu monitor findings ID`
lists a want's findings the same as a watch's. The web UI has the same on the monitor page,
and the catalog's All tab is the same search by hand.

A `--profile` on a watch or a want is resolved when it is added, against `--runtime` or the
slot's runtime, and kept by id, so an unknown or mismatched profile is refused at once and a
renamed one still swaps. A want's `--format`, `--runtime`, and `--kind` are checked the same way
when it is added. Removing a profile a watch or want names is refused until they are pointed
elsewhere or the removal is forced, which clears the reference; removing a slot they swap into
drops the swap when forced, and removing a source a watch checks through removes the watch and
widens a want narrowed to it when forced.

## Notifications

Every finding reaches the web UI as a toast on whatever page is open, and the settings page
can turn on desktop notifications for this browser. `notify.webhooks` in config posts one JSON
document, the event as the UI sees it, to each URL for every new finding and for every instance
that fails, so a chat bot or an automation can pick it up. Posts queue behind a slow webhook
rather than holding the daemon's event stream, a connection failure or a server error is tried
again a few times with a growing pause, and anything the queue cannot hold is counted in the log.
