# Command line

Every command talks to a daemon. `nebu serve` runs one. Commands that only need the
host, sources, or the store start a daemon in process when none is listening on the
configured address, so `inspect`, `pull`, `list`, and `build` work without `serve`. Commands
whose state lives in the daemon, such as `run`, `ps`, `slots`, and `monitor`, need `serve`
running and find it on the configured listen address without any flag.

Global flags come before the command: `--config PATH`, `--addr HOST:PORT`, `--json`.

## Host and catalog

```
nebu doctor                          probe the host and check every dependency
nebu host [--refresh]                the probed profile
nebu sources                         configured sources
nebu search [--source S] [--tag T] [--limit N] words
nebu inspect org/repo[@rev] [--source S] [--runtime R] [--group G] [--ctx N] [--param k=v]
```

## Store

```
nebu pull org/repo[@rev] --group G [--source S] [--detach]
nebu list
nebu remove org/repo [--group G] [--gc]
nebu store status | gc [--partials] | verify [repo]
nebu store export --dir DIR [repo] [--source S] [--group G]
```

## Runtimes and builds

```
nebu runtimes list                   manifests and host compatibility
nebu runtimes installs [runtime]
nebu runtimes adopt RUNTIME [--path P]
nebu runtimes install RUNTIME        download the prebuilt release the host rules select
nebu runtimes remove INSTALL
nebu runtimes recipes [runtime]      recipes with the variant this host selects
nebu build RUNTIME [--recipe R] [--variant V] [--var k=v] [--sandbox host|oci] [--image I] [--ref REF] [--force] [--detach]
nebu builds list [runtime] | show ID | remove ID
```

## Running models

```
nebu run org/repo [--group G] [--runtime R] [--install I] [--name N] [--slot S] [--param k=v]
nebu ps [--all]
nebu show NAME|ID
nebu logs NAME|ID [--follow] [--tail N]
nebu stop NAME|ID
```

## Slots and swaps

```
nebu slots list
nebu slots create NAME [--device ID]... [--memory 8GiB] [--runtime R] [--param k=v] [--description D]
nebu slots show NAME
nebu slots update NAME [same flags as create, only the flags passed change]
nebu slots evict NAME                stop the occupant, keep the slot and its name
nebu slots remove NAME [--force]
nebu swap SLOT org/repo [--group G] [--runtime R] [--install I] [--param k=v] [--drain-first]
```

## Gateway

```
nebu gateway                         listeners, routes, counters
nebu routes list
nebu routes add NAME INSTANCE        an alias onto a running instance
nebu routes remove NAME
```

## Monitor

```
nebu monitor list
nebu monitor add org/repo [--source S] [--revision R] [--match REGEX] [--auto-pull] [--slot S] [--runtime R] [--param k=v]
nebu monitor remove ID|repo
nebu monitor check [ID]
nebu monitor findings [ID] [--unacked]
nebu monitor ack FINDING
```

## Tasks and events

```
nebu tasks list [--active] | watch ID | cancel ID
nebu events [--snapshot] [--kind K]...   JSON lines of every change the daemon publishes
```
