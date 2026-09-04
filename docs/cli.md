# Command line

Every command talks to a daemon. `nebu serve` runs one. Commands that only need the
host, sources, the store, installs, or builds start a daemon in process when none is listening
on the configured address, so `inspect`, `pull`, `list`, `runtimes`, `build`, and `builds` work
without `serve`. Commands whose state lives in the daemon, such as `run`, `ps`, `slots`,
`routes`, `chat`, `monitor`, `events`, `tasks watch`, and `sources add`, need `serve` running,
find it on the configured listen address without any flag, and refuse before building anything
when nothing answers. When `tls.cert_file` is set the CLI dials `https` and trusts that
certificate as it is.

Global flags come before the command: `--config PATH`, `--addr HOST:PORT`, `--json`.

## Host and catalog

```
nebu doctor                          probe the host and check every dependency
nebu host [--refresh]                the probed profile
nebu sources                         configured sources with their sorts, facets, and auth state
nebu sources providers               providers with the settings their sources accept
nebu sources add ID --kind K [--name N] [--set name=value]...
nebu sources update ID [--name N] [--set name=value]... [--unset name]...
nebu sources remove ID [--force]     refused while a watch or want names the source, --force drops them
nebu search [--source S | --kind K] [--sort ID] [--asc] [--filter facet=value]... [--tag T]... [--author A] [--limit N] [--cursor C] [words]
                                     every source at once with neither --source nor --kind
nebu revisions REPO [--source S]     branches, tags, versions, or variants of a repository
nebu card REPO[@rev] [--source S]    the model card a source publishes
nebu inspect REPO[@rev] [--source S] [--runtime R] [--group G] [--ctx N] [--slot S] [--profile P] [--param k=v]
```

`inspect` prints two verdicts per row: `TOTAL` plans against the whole device memory, what a run
gets once nothing else is loaded, and `FREE NOW` against what is free at this moment, which is
what `nebu run` plans against. Every row layers params the way a run does, the runtime's default
profile, then the slot's defaults with `--slot`, then `--param`, with the same learned overhead
correction a run applies. `--profile` starts from a named profile instead and plans its runtime
alone unless `--runtime` names others, which start from their own defaults.

`search` with neither `--source` nor `--kind` searches every source at once, each provider's
order interleaved and every hit marked with its source, and sorts and facets are left out since
they belong to one provider. `search` without words browses the source in its default order. Sorts and facets differ per source
and `nebu sources` lists them; a facet filter such as `--filter task=text-generation` or
`--filter type=LORA,Checkpoint` narrows the listing, and `--cursor` continues from the value the
previous page printed. Most catalogs only order descending, so `--asc` works with the sorts
`nebu sources` marks with `±` and is refused for the rest. `--kind` searches every source of a
provider at once, each hit marked with its source and a warning for any source that did not
answer. Repository forms differ per source too, see [config.md](config.md).

`sources add` names the provider with `--kind`: `huggingface`, `modelscope`, `ollama`, `civitai`,
`oci`, `kaggle`, `ngc`, `csghub`, `github`, `git`, `local`, or `mirror`. Settings go in as
`--set name=value`, and `nebu sources providers` prints which names each provider accepts with
their defaults, required ones starred: `local` needs `--set path=DIR`, `git` needs
`--set endpoint=URL`, `mirror` needs `path` or `endpoint`. A setting left out means the provider
default. `sources update` merges `--set` into what the source has and `--unset` drops a setting
back to its default. Seeded defaults take `update` and refuse `remove`. The first source listed
is the one `--source` falls back to: the oldest one you added, or `huggingface`.

## Store

```
nebu pull org/repo[@rev] --group G [--source S] [--detach]
nebu list
nebu remove org/repo [--group G] [--source S] [--gc]
nebu store status | gc [--partials] | verify [repo] [--source S] [--group G]
nebu store export --dir DIR [repo] [--source S] [--group G]
```

## Runtimes and builds

```
nebu runtimes list                   manifests and host compatibility
nebu runtimes show RUNTIME           one manifest with every param, type, default, and choices
nebu runtimes installs [runtime]
nebu runtimes adopt RUNTIME [--path P]
nebu runtimes install RUNTIME        download the prebuilt release the host rules select
nebu runtimes remove INSTALL
nebu runtimes recipes [runtime]      recipes with the variant this host selects
nebu profiles [runtime]              named param sets per runtime, the default marked
nebu profiles add RUNTIME NAME [--description D] [--param k=v]... [--default]
nebu profiles update ID|NAME [--runtime R] [--name N] [--description D] [--param k=v]... [--unset k]... [--default=BOOL]
nebu profiles remove ID|NAME [--runtime R] [--force]
nebu build [RUNTIME] [--recipe R] [--variant V] [--var k=v] [--sandbox host|oci] [--image I] [--ref REF] [--force] [--detach]
                                     RUNTIME may be left out when --recipe names the recipe
nebu builds list [runtime] | show ID | remove ID
nebu version
```

A profile is a named set of params for one runtime, kept as rows beside the seeded manifest.
`nebu runtimes list` names the runtimes, `nebu runtimes show RUNTIME` prints every param with
its type, default, choices, and description, and the run dialog shows the same. `profiles add` refuses a param the runtime does not declare or a value
of the wrong type. One profile per runtime can be the default: every run, swap, and fit check of
that runtime that names no profile starts from it. `profiles update` merges `--param` into what
the profile has and `--unset` drops a param back to the manifest default. A name is unique within
a runtime, so `--runtime` disambiguates when two runtimes share one. Runs, watches, wants, and
slots keep a profile by id, so renaming one changes nothing that names it. `profiles remove`
refuses while a watch, want, slot request, or instance names the profile and lists them,
`--force` clears those references first so they start from the runtime's default profile.

## Running models

```
nebu run org/repo [--group G] [--source S] [--runtime R] [--install I] [--name N] [--slot S] [--profile P] [--param k=v] [--force]
nebu ps [--all]
nebu show NAME|ID
nebu logs NAME|ID [--follow] [--tail N]
nebu stop NAME|ID
```

Params layer in a fixed order: the runtime's default profile, or the one `--profile` names by id
or name, then the slot's default params, then `--param`. A profile picks its runtime when neither
`--runtime` nor a slot does. `--force` launches even when the plan says the model does not fit and
redoes any prepare step the runtime has, and it is kept on the record so a relaunch after a
restart and a rollback keep it. A name that is a slot's is refused for a plain run.

## Slots and swaps

```
nebu slots list
nebu slots create NAME [--device ID]... [--memory 8GiB] [--runtime R] [--param k=v] [--description D] [--max-in-flight N] [--rps R] [--burst N] [--timeout D] [--upstream-timeout D]
nebu slots show NAME
nebu slots update NAME [same flags as create, only the flags passed change]
nebu slots evict NAME                stop the occupant, keep the slot and its name
nebu slots remove NAME [--force]     refused while it serves, or a watch or want swaps into it, --force stops and drops them
nebu swap SLOT org/repo [--group G] [--source S] [--runtime R] [--install I] [--profile P] [--param k=v] [--drain-first] [--force]
```

## Gateway

```
nebu gateway                         listeners, routes, counters, and the limits each route enforces
nebu routes list
nebu routes add NAME INSTANCE        an alias onto a running instance
nebu routes remove NAME
nebu chat MODEL [--system S] [--once TEXT] [--temperature F] [--max-tokens N] [--key K]
```

`chat` talks to a route through the gateway the way a client would, at the address the daemon
reports for it, failing before the first turn when no route of that name is ready and naming the
ones that are, then streaming the answer to stdout with a token count and rate after it. Without
`--once` it reads turns from stdin and keeps the history, `/reset` clears it, `/system TEXT` sets
the prompt and keeps the turns, ctrl-c stops the answer in flight and keeps what arrived, and a
runtime that breaks off mid answer is reported as an error. The first `gateway.api_keys` entry is
sent unless `--key` says otherwise.

## Monitor

```
nebu monitor list
nebu monitor add org/repo [--source S] [--revision R] [--match REGEX] [--auto-pull] [--slot S] [--runtime R] [--profile P] [--param k=v]
nebu monitor remove ID|repo
nebu monitor check [ID] [--rearm]     one watch or want, or every watch and open want
nebu monitor findings [ID] [--unacked]  of one watch by id or repo, or one want by id
nebu monitor ack FINDING
nebu monitor wants                   wanted models and what was found
nebu monitor want WORDS... [--kind K | --source S] [--format F] [--match REGEX] [--auto-pull] [--slot S] [--runtime R] [--profile P] [--param k=v]
nebu monitor unwant ID
```

## Tasks and events

```
nebu tasks list [--active] | watch ID | cancel ID
nebu events [--snapshot] [--kind K]...   JSON lines of every change the daemon publishes
```
