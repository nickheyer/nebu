# nebu

Nebu manages model weights and inference runtimes. Named after the Nebuchadnezzar from The Matrix.

- Runtimes: llama.cpp, vLLM, SGLang, NeMo, stable-diffusion.cpp
- Diffusers: image and video generation
- Runtime installation: use an existing binary, download one, or build from source
- Model sources: Hugging Face, GitHub, Ollama, ModelScope, Civitai, Kaggle, NGC, CSGHub, OCI registries, git, mirrors, and local directories
- APIs: OpenAI, Anthropic, Ollama
- Discord: text, images, video, personas, automations, and sharding
- Platforms: Linux, macOS, Windows, and FreeBSD

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
