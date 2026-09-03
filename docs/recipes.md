# Recipes and sandboxes

`nebu build RUNTIME` turns a recipe into an install of kind `built`. A recipe is YAML under
`spec/recipes/` decoding into the `Recipe` message.

```yaml
id: llamacpp
runtime_id: llamacpp
source:
  release: https://api.github.com/repos/ggml-org/llama.cpp/releases?per_page=5
  ref: latest                                   # resolved to the newest tag in the feed
  archive: https://github.com/ggml-org/llama.cpp/archive/refs/tags/{{.ref}}.tar.gz
tools: [cmake]                                  # must be on PATH for a host build
facts: [device.vendor, device.compute_capability, nvidia.driver_version]
vars:
  build_type: Release
variants:
  - id: cuda
    when: any(devices, .vendor == "nvidia")     # first matching variant wins
    tools: [nvcc]
    vars:
      backend: -DGGML_CUDA=ON
      archs: '-DCMAKE_CUDA_ARCHITECTURES=...'   # rendered from probed compute capabilities
  - id: cpu
    when: 'true'
sandbox:
  kind: SANDBOX_KIND_HOST
  cli: [podman, docker, nerdctl]
steps:
  - name: configure
    command: [cmake, -S, ., -B, build, '{{.vars.backend}}', '{{.vars.archs}}']
  - name: compile
    command: [cmake, --build, build, --target, llama-server, -j, '{{.jobs}}']
outputs: [build/bin/llama-server]
binary: llama-server
```

## What happens

1. The host profile picks the variant. An explicit `--variant` overrides it. A variant whose
   tools are missing is skipped in favour of the next one that matches, and reported by
   `nebu runtimes recipes` and `nebu doctor`.
2. `vars` render in key order against the host env, the variant, and the ref, so later vars
   can reference earlier ones. `--var k=v` overrides any of them.
3. The ref resolves. `latest` reads the release feed and takes the newest tag that is not a
   draft or prerelease.
4. Everything that decides the bytes produced is hashed: the recipe, variant, vars, ref,
   sandbox and image, the selected host facts, and the patch contents. The hash is the
   build id and its directory under `builds.dir`. A finished build with that id whose binary
   and install still exist is a cache hit and `nebu build` returns at once. `--force` rebuilds.
5. The source is fetched, an archive through the download cache and unpacked, or a git
   repository cloned at the ref. Recipes with no source get an empty tree.
6. Patches whose `when` holds apply in order with a pure Go unified diff applier. Content is
   inline, a file under `spec/patches/`, or a URL. A hunk already present is skipped.
7. Steps run in the sandbox, each rendered with `.dir` (the tree), `.out` (the output
   directory), `.root`, `.jobs`, `.ref`, `.commit`, `.vars`, and the host env. Empty rendered
   arguments are dropped, so a var set to an empty string, or `index .vars "name"` on a var
   that was never set, disappears from the command line. A step `when` can skip a step.
8. Outputs are copied into `out/`, the binary must be among them, and an install is
   recorded with the manifest probes run against it.

## Sandboxes

`SANDBOX_KIND_HOST` runs steps directly with the host toolchain. `SANDBOX_KIND_OCI` runs
each step as `CLI run --rm -v BUILD:/work -w /work/src IMAGE ...` through the first of
`sandbox.cli` found on PATH, so podman, docker, and nerdctl work unchanged. The image comes
from `--image`, the variant, the recipe, or `builds.image` in config. Git sources are still
cloned on the host. Pass `--sandbox oci --image IMAGE` on the command line or set
`builds.sandbox` in config to make it the default.

The transcript of every build is streamed to its task and written to `build.log` in the
build directory. `nebu builds show ID` prints the record, `nebu builds remove ID` deletes the
tree and the install it produced.
