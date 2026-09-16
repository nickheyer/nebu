# nebu
Nebu is a model loading shim for inference services like llama.cpp, vLLM, SGLang, and NeMo

# Intro

Nebu (named after Nebuchadnezzar, the matrix spaceship, not the babylonian guy) is yet another time-saving solution for a niche engineering problem - this time it's managing open source model weights and coordinating with backend inference platforms like `llama.cpp`. 

- Four runtimes: llama.cpp, vLLM, SGLang, and NeMo.
- Four runtime installers: Adopt binary, Download binary, Build Binary
- Model sources: Hugging Face, GitHub, Ollama, ModelScope, Civitai, Kaggle, NGC, CSGHub, OCI registries, git, mirrors, and local directories
- API Dialects: OpenAI, Anthropic, and Ollama
- Platforms: Linux, macOS, Windows, and FreeBSD

# Install

On Arch Linux:

```sh
yay -S nebu-bin # or nebu for full build
systemctl --user enable --now nebu.service
```

For everyone else:

Releases available for your platform at [GitHub Releases](https://github.com/nickheyer/nebu/releases)

## Build

Requires Go 1.27, docker, and make.

```sh
make gen
make build
```

See `Makefile` for all commands, there are many.

