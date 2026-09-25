# nebu

Nebu manages model weights and inference runtimes. Named after the Nebuchadnezzar from The Matrix.

- Runtimes: llama.cpp, vLLM, SGLang, NeMo, stable-diffusion.cpp
- Diffusers: image and video generation
- Runtime installation: use an existing binary, download one, or build from source
- Model sources: Hugging Face, GitHub, Ollama, ModelScope, Civitai, Kaggle, NGC, CSGHub, OCI registries, git, mirrors, and local directories
- APIs: OpenAI, Anthropic, Ollama
- Discord: text, images, video, personas, automations, and sharding
- Platforms: Linux, macOS, Windows, and FreeBSD
- SSO/OIDC: Supports all OpenID Connect Providers. See `compose.yaml` or `packaging/config.yaml`
- Local Auth: Enabled by default. The web UI signs in with an account, API and gateway clients send an API token made in Settings

# Install

On Arch Linux:

```sh
yay -S nebu-bin # or nebu to build from source
systemctl --user enable --now nebu.service
```

For other platforms, download a binary from [GitHub Releases](https://github.com/nickheyer/nebu/releases).

## Build

Requires Go 1.27, Docker, and Make.

```sh
make gen
make build
```

See [Makefile](Makefile) for other commands.

## Mesh

Several nebu daemons on one network pool their devices: a model that fits no single machine runs across several, and a model that fits one gains speed from the others. Membership runs from the Mesh page of any node: make a mesh on one, and the others show up there as they are heard on the network, to be invited or to ask; any member admits. See [MESH.md](MESH.md) for the shapes, the planner, and the settings under `mesh:` in [packaging/config.yaml](packaging/config.yaml).
