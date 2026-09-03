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
sources:
  - id: huggingface
    kind: SOURCE_KIND_HUGGINGFACE
    token_env: HF_TOKEN
  - id: modelscope
    kind: SOURCE_KIND_MODELSCOPE
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
