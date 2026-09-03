# Monitor

The monitor watches repositories for new revisions and weight groups.

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
swapped onto the freshest pull, preferring the group it already serves. The pull and swap
tasks are attached to the findings, and everything is visible on the monitor page of the
web UI, where findings stay until acknowledged.
