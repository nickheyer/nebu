# Configuration

The config file is YAML that decodes into the `Config` message in `proto/nebu/v1/config.proto`.
It is looked up as `$NEBU_CONFIG`, then `./nebu.yaml`, then `$XDG_CONFIG_HOME/nebu/config.yaml`,
then `/etc/nebu/config.yaml`. Unknown keys fail the load.

```yaml
listen: 127.0.0.1:8484            # api, gateway, and web ui
data_dir: ~/.local/share/nebu     # db, runtime output, installs, builds
cache_dir: ~/.cache/nebu
store_dir: <data_dir>/store       # blobs, manifests, links
spec_dirs: [/etc/nebu/spec]       # extra spec layers, <data_dir>/spec is always last
sources:                          # bootstrap: each entry is created once, when no source has its name
  - id: hf-mirror                  # a second Hugging Face source beside the seeded default
    kind: SOURCE_KIND_HUGGINGFACE
    endpoint: https://hf-mirror.com
  - id: ghcr                       # a second registry beside the seeded dockerhub one
    kind: SOURCE_KIND_OCI
    endpoint: https://ghcr.io
  - id: mirror
    kind: SOURCE_KIND_MIRROR
    endpoint: https://mirror.example/models   # or path: /mnt/mirror
  - id: local
    kind: SOURCE_KIND_LOCAL
    path: /srv/models
contexts: [8192, 32768, 131072]   # context lengths inspect plans for
min_free_bytes: 53687091200       # storage warning threshold for doctor
transfer:
  workers: 8
  chunk_bytes: 33554432
  retries: 5
  max_bytes_per_second: 0
gateway:
  listen: ""                      # its own address, else shared with listen
  api_keys: []                    # bearer keys required on /v1 when set
  api_key_env: NEBU_API_KEYS      # comma separated keys from the environment
  drain_timeout_ms: 30000         # how long a swap waits for in flight requests
auth:
  token: ""                       # bearer token required on the api when set
  token_env: NEBU_TOKEN
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

When `auth.token` is set every API call, from the CLI or the web UI, must carry it as a
bearer token. The CLI reads it from the same config or `NEBU_TOKEN`. The web UI asks for it
on the settings page and keeps it in the browser.

## Sources

A source is a configured instance of a provider. Providers are built in: Hugging Face,
ModelScope, Ollama, Docker Hub, Civitai, Kaggle, NGC, CSGHub, a host filesystem, a nebu mirror.
Every provider answers the same catalog interface: browse or search with a sort and facets, page
through results, resolve a repository at a revision to its files, list its revisions, read its
card, and open files for ranged reads.

Sources are rows in the daemon's database. Providers that work without configuration get one
default source seeded under the provider's name: `huggingface`, `modelscope`, `ollama`,
`civitai`, `dockerhub`, `kaggle`, `ngc`, `csghub`. The `sources` list in config is a bootstrap:
each entry creates a source with that id when none exists and is otherwise ignored, so once a
source exists the web UI owns it. Create, edit, and remove sources on the settings page.
`nebu sources` prints every source with what its provider can do.

All of the seeded defaults list and search without a token. Civitai and Kaggle need one to
download anything, the rest only for gated or private repositories. The first source is the one
commands fall back to when `--source` is not given: your first configured entry, or
`huggingface` with no config.

What a source of each kind needs:

| kind | endpoint default | token env default | repo form | revision |
| --- | --- | --- | --- | --- |
| `SOURCE_KIND_HUGGINGFACE` | `https://huggingface.co` | `HF_TOKEN` | `org/model` | branch or tag, `main` |
| `SOURCE_KIND_MODELSCOPE` | `https://www.modelscope.cn` | `MODELSCOPE_API_TOKEN` | `org/model` | branch or tag, `master` |
| `SOURCE_KIND_OLLAMA` | `https://ollama.com`, blobs from `registry.ollama.ai` | `OLLAMA_API_KEY` | `name[:tag]`, `latest` when absent | tag |
| `SOURCE_KIND_OCI` | `https://hub.docker.com`, blobs from `registry-1.docker.io` | `DOCKER_TOKEN` as `user:token` | `namespace/name[:tag]`, `ai/` when absent | tag |
| `SOURCE_KIND_CIVITAI` | `https://civitai.com` | `CIVITAI_API_TOKEN` | model id, or `id/versionId`, or a model page URL | version id or name |
| `SOURCE_KIND_KAGGLE` | `https://www.kaggle.com` | `KAGGLE_KEY`, basic auth with `KAGGLE_USERNAME` when set | `owner/model[/framework/instance]` | version number |
| `SOURCE_KIND_NGC` | `https://api.ngc.nvidia.com` | `NGC_API_KEY`, exchanged at `authn.nvidia.com`, guest when absent | `org[/team]/name` | version |
| `SOURCE_KIND_CSGHUB` | `https://hub.opencsg.com` | `OPENCSG_TOKEN` | `namespace/name` | branch, `main` |
| `SOURCE_KIND_MIRROR` | `endpoint` or `path` required | `token_env` as configured | `org/model` | fixed by the export |
| `SOURCE_KIND_LOCAL` | `path` required | none | directory under `path` | none |

Sources that keep several variants under one name, such as an Ollama tag, a Docker tag, a Civitai
version, or a Kaggle instance, resolve to a repository that names the variant, so `llama3.2` at tag
`3b` is stored as `llama3.2:3b`. The catalog lists those variants under the repository and switches
between them. Anything a catalog lets you narrow by, such as Civitai's adult content switch or
Ollama's capability filter, is a facet the source declares and `nebu sources` lists; it is not
config.

Any hub that speaks the Hugging Face API, such as `hf-mirror.com` or an enterprise Hub, is a
`SOURCE_KIND_HUGGINGFACE` source with its `endpoint` set. Any registry that speaks the OCI
distribution API, including GHCR and a private registry, is a `SOURCE_KIND_OCI` source with its
`endpoint` set. Any CSGHub install is a `SOURCE_KIND_CSGHUB` source with its `endpoint` set.
