# Configuration

The config file is YAML that decodes into the `Config` message in `proto/nebu/v1/config.proto`.
It is looked up as `$NEBU_CONFIG`, then `./nebu.yaml`, then `$XDG_CONFIG_HOME/nebu/config.yaml`,
then `/etc/nebu/config.yaml`. Unknown keys fail the load.

```yaml
listen: 127.0.0.1:8484            # api, gateway, and web ui
addr: ""                          # daemon address the cli dials, in process when empty
data_dir: ~/.local/share/nebu     # db, runtime output, installs, builds
cache_dir: ~/.cache/nebu
store_dir: <data_dir>/store       # blobs, manifests, links
store:
  max_bytes: 0                    # cap on blob bytes, kept by evicting before a pull, 0 for none
spec_dirs: [/etc/nebu/spec]       # extra spec layers, <data_dir>/spec is always last
sources:                          # bootstrap: each entry is created once, when no source has its name
  - id: hf-mirror                  # a second Hugging Face source beside the seeded default
    kind: SOURCE_KIND_HUGGINGFACE
    name: HF Mirror
    config:
      endpoint: https://hf-mirror.com
  - id: ghcr                       # a second registry beside the seeded dockerhub one
    kind: SOURCE_KIND_OCI
    config:
      registry_endpoint: https://ghcr.io
  - id: mirror
    kind: SOURCE_KIND_MIRROR
    config:
      endpoint: https://mirror.example/models   # or path: /mnt/mirror
  - id: local
    kind: SOURCE_KIND_LOCAL
    config:
      path: /srv/models
contexts: [8192, 32768, 131072]   # context lengths inspect plans for
min_free_bytes: 53687091200       # storage warning threshold for doctor
transfer:
  workers: 8
  chunk_bytes: 33554432
  retries: 5
  max_bytes_per_second: 0         # outside every window, 0 for no limit
  windows: []                     # spans of the week with their own limit, first that holds wins
  # - days: mon-fri               # names or ranges, every day when empty
  #   from: "09:00"               # local HH:MM, from after to wraps past midnight
  #   to: "18:00"
  #   max_bytes_per_second: 5242880
  # - days: sun
  #   pause: true                 # holds downloads, they resume when the window ends
gateway:
  listen: ""                      # its own address, else shared with listen
  api_keys: []                    # bearer keys required on /v1 when set
  api_key_env: NEBU_API_KEYS      # comma separated keys from the environment
  drain_timeout_ms: 30000         # how long a swap waits for in flight requests
  cors_origins: []                # origins browsers may call from, any when empty
  policy:                         # limits every route enforces, a slot can set its own
    max_in_flight: 0              # requests at once, 0 for no cap
    requests_per_second: 0        # sustained rate, 0 for no limit
    burst: 0                      # absorbed at once, the rate rounded up when 0
    request_timeout_ms: 0         # the whole exchange, 0 for none
    upstream_timeout_ms: 600000   # how long the runtime may take to start answering
auth:
  token: ""                       # bearer token required on the api when set
  token_env: NEBU_TOKEN
tls:
  cert_file: ""                   # serve the api, web ui, and gateway over TLS when set
  key_file: ""
notify:
  webhooks: []                    # URLs posted one JSON event per new finding and failed instance
monitor:
  interval_ms: 3600000            # how often watches are checked
  disabled: false
builds:
  sandbox: SANDBOX_KIND_HOST      # or SANDBOX_KIND_OCI
  image: ""                       # default container image for oci builds
  cli: [podman, docker, nerdctl]
  jobs: 0                         # parallel compile jobs, cpu count when 0
  dir: <data_dir>/builds
web:
  disabled: false
logging:
  level: info
  format: text
  file: ""
```

Environment overrides: `NEBU_ADDR`, `NEBU_DATA_DIR`, `NEBU_LISTEN`, `NEBU_TOKEN`.

The database under `data_dir` is `nebu.db`, SQLite through pure Go. Its schema lives in
`internal/db/schema.sql` and atlas turns that into the migration directory the daemon applies at
start, one init migration until release, so `make migrate-reset` regenerates it whenever the
schema changes and `make migrate-validate` checks the directory and its `atlas.sum`. A database
written by a nebu older than the atlas migrations is set aside as `nebu.db.pre-atlas.<time>` and
a fresh one is started, with a warning in the log naming the copy. A database whose recorded
revision the rewritten directory no longer holds is brought to the head in place, its tables
diffed against `schema.sql` and its rows kept, and its revision replaced by a baseline, so
`make migrate-reset` never strands a database.

`store.max_bytes` caps the blobs on disk. Before a pull fetches anything the store removes the
models that have sat unused longest, a model's last use being the last run that started from
it, until the bytes to come fit under the cap. Whatever is running, whatever a slot would
relaunch, and the model being pulled are never evicted, and a single pull larger than the cap
still proceeds after evicting what it may. Eviction and `nebu store gc` wait for pulls in
flight, whose blobs have no manifest yet, and eviction drops only the blobs of the models it
removed that nothing else names. `nebu store status` and the store page show the cap beside
what is on disk, and `nebu list` and the store page show when each model was last used.

`transfer.windows` shape downloads by time of week. Each window names days, a local clock
span, and either its own byte rate or `pause`, which holds every download until the window
ends; outside every window `max_bytes_per_second` applies. A pull in flight follows the
schedule as the clock moves, so a rate set for office hours eases at night without a restart.
The limits cover every download nebu makes: pulls, prebuilt runtime archives, and the source
archives and patches a recipe fetches. A paused window is waited out before a connection
opens, so nothing idles on a server through the pause, and a file the Hugging Face CLI or git
LFS moves whole waits for the window to end before it starts.

When `auth.token` is set every API call, from the CLI or the web UI, must carry it as a
bearer token. The CLI reads it from the same config or `NEBU_TOKEN`. The web UI asks for it
on the settings page and keeps it in the browser.

`tls.cert_file` and `tls.key_file` put the API, web UI, and gateway listeners behind TLS with
HTTP/2. The CLI reading the same config dials `https` and trusts that certificate as it is,
so a self signed one needs no flag, while any other certificate must verify against the
system roots for the host dialed.

`addr` is where the CLI finds the daemon, `NEBU_ADDR` or `--addr` over it. Empty means the
`listen` address when something answers there and an in process daemon otherwise. A host
left unspecified, such as `0.0.0.0`, is dialed as `localhost`, and a gateway listener bound
that way is dialed on the API's host.

## Sources

A source is a configured instance of a provider. Providers are built in: Hugging Face,
ModelScope, Ollama, an OCI registry, Civitai, Kaggle, NGC, CSGHub, GitHub, a git host, a host
filesystem, a nebu mirror. Every provider answers the same catalog interface: browse or search with
a sort and facets, page through results, resolve a repository at a revision to its files, list its
revisions, read its card, and open files for ranged reads.

Sources are rows in the daemon's database. Providers that work without configuration get one
default source seeded under the provider's name: `huggingface`, `modelscope`, `ollama`,
`civitai`, `dockerhub`, `kaggle`, `ngc`, `csghub`, `github`. A seeded default can be edited, to
point it at a proxy or another token variable, but never removed. The `sources` list in config is a
bootstrap: each entry creates a source with that id when none exists and is otherwise ignored, so
once a source exists the daemon owns it. Config entries are created before the seeds, so an entry
named after a provider defines that provider's source instead of the seeded one. Create, edit, and
remove sources with `nebu sources add`, `update`, and `remove`, or on the settings page. `nebu
sources` prints every source with what its provider can do and marks which are seeded.

A source's kind is fixed when it is created; to move to another provider, remove it and add a new
source. Its settings are a map of names to values. Each provider declares the settings its sources
accept, one group per transport it moves bytes through, each with a type, a default, and whether a
source must set it; `nebu sources providers` prints the declaration and the settings page renders
its form from it. A setting left out means the provider default, which is what every seeded source
starts with, and a name the provider does not declare is refused. Ids are plain names of letters,
digits, dots, dashes, and underscores, because the store keeps what a source pulled under its id,
and what a source pulled stays in the store after the source is removed. A source whose settings
stop working, such as a directory that went away, stays listed with its error rather than keeping
the daemon from starting.

Transports are the ways bytes and listings move, one Go type each: `http` with an endpoint and a
token variable, `distribution` for an OCI registry with a credential variable holding `user:secret`,
`file` for a directory, `git` for a partial clone whose LFS pointers resolve through the batch
API, and `hfcli` for the Hugging Face CLI. A provider names the transports it uses; the primary
one owns the bare setting names and every other one prefixes its settings with its name, so an OCI
source has `endpoint` for the hub API and `registry_endpoint` for the registry. A URL setting needs
a scheme, a variable setting is an environment variable name, and a path is a directory.

All of the seeded defaults list and search without a token. Civitai and Kaggle need one to
download anything, the rest only for gated or private repositories. The first source is the one
commands fall back to when `--source` is not given: your first configured entry, or
`huggingface` with no config.

What a source of each kind accepts, defaults in parentheses, required settings starred:

| kind | settings | repo form | revision |
| --- | --- | --- | --- |
| `SOURCE_KIND_HUGGINGFACE` | `endpoint` (`https://huggingface.co`), `token_env` (`HF_TOKEN`), `cli_command` (empty, `hf` or `huggingface-cli` moves whole files through the CLI) | `org/model` | branch or tag, `main` |
| `SOURCE_KIND_MODELSCOPE` | `endpoint` (`https://www.modelscope.cn`), `token_env` (`MODELSCOPE_API_TOKEN`) | `org/model` | branch or tag, `master` |
| `SOURCE_KIND_OLLAMA` | `endpoint` (`https://ollama.com`), `token_env` (`OLLAMA_API_KEY`), `registry_endpoint` (`https://registry.ollama.ai`) | `name[:tag]`, `latest` when absent | tag |
| `SOURCE_KIND_OCI` | `endpoint` (`https://hub.docker.com`), `registry_endpoint` (`https://registry-1.docker.io`), `registry_token_env` (`DOCKER_TOKEN` as `user:token`) | `namespace/name[:tag]`, `ai/` when absent | tag |
| `SOURCE_KIND_CIVITAI` | `endpoint` (`https://civitai.com`), `token_env` (`CIVITAI_API_TOKEN`) | model id, or `id/versionId`, or a model page URL | version id or name |
| `SOURCE_KIND_KAGGLE` | `endpoint` (`https://www.kaggle.com`), `token_env` (`KAGGLE_KEY`), `username_env` (`KAGGLE_USERNAME`, basic auth) | `owner/model[/framework/instance]` | version number |
| `SOURCE_KIND_NGC` | `endpoint` (`https://api.ngc.nvidia.com`), `token_env` (`NGC_API_KEY`, exchanged at `auth_endpoint`, guest when absent), `auth_endpoint` (`https://authn.nvidia.com`) | `org[/team]/name` | version |
| `SOURCE_KIND_CSGHUB` | `endpoint` (`https://hub.opencsg.com`), `token_env` (`OPENCSG_TOKEN`) | `namespace/name` | branch, `main` |
| `SOURCE_KIND_GITHUB` | `endpoint` (`https://api.github.com`), `token_env` (`GITHUB_TOKEN`), `git_endpoint` (`https://github.com`), `git_token_env` (`GITHUB_TOKEN`) | `owner/repo` | a release tag resolves to its assets, a branch, tag, or commit to the tree, the newest stable release when empty |
| `SOURCE_KIND_GIT` | `endpoint`* (the host repositories are cloned under), `token_env`, `username_env` | `owner/repo` under the endpoint | branch or tag, the default branch when empty |
| `SOURCE_KIND_MIRROR` | `endpoint` or `path`, one of them, `token_env` | `org/model` | fixed by the export |
| `SOURCE_KIND_LOCAL` | `path`* | directory under `path` | none |

What each catalog holds decides which runtime serves it. Hugging Face, ModelScope, CSGHub, Kaggle,
GitHub, git hosts, and mirrors hold GGUF files and transformers checkpoints, served by llama.cpp and
vLLM; Kaggle instances under other frameworks list but do not read. NGC holds NeMo 2 checkpoint
directories and packed `.nemo` archives, served by NeMo, which converts an archive once before its
first run, beside TAO, Riva, TensorRT, and MONAI assets that only browse. Civitai's image
checkpoints browse only. GitHub also serves the release assets runtimes install from, see
[recipes.md](recipes.md).

Sources that keep several variants under one name, such as an Ollama tag, a Docker tag, a Civitai
version, or a Kaggle instance, resolve to a repository that names the variant, so `llama3.2` at tag
`3b` is stored as `llama3.2:3b`. The catalog lists those variants under the repository and switches
between them. Anything a catalog lets you narrow by, such as Civitai's adult content switch or
Ollama's capability filter, is a facet the source declares and `nebu sources` lists; it is not
config.

Any hub that speaks the Hugging Face API, such as `hf-mirror.com` or an enterprise Hub, is a
`SOURCE_KIND_HUGGINGFACE` source with its `endpoint` set. Any registry that speaks the OCI
distribution API, including GHCR and a private registry, is a `SOURCE_KIND_OCI` source with its
`registry_endpoint` set. Any CSGHub install is a `SOURCE_KIND_CSGHUB` source with its `endpoint`
set. Any forge that serves git over HTTPS with LFS, such as GitLab, Gitea, or Codeberg, is a
`SOURCE_KIND_GIT` source with its `endpoint` set. Two sources of one provider merge into one
listing in the catalog, each hit marked with the source it came from.
