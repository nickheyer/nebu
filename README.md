# nebu
Nebu is a model loading shim for inference services like llama.cpp, vLLM, SGLang, and NeMo

# Intro

Nebu (named after Nebuchadnezzar, the matrix spaceship, not the babylonian guy) is yet another time-saving solution for a niche engineering problem - this time it's managing open source model weights and coordinating with backend inference platforms like `llama.cpp`. 

- Four runtimes, llama.cpp, vLLM, SGLang, and NeMo, each installed by a method it lists, a binary on the host, a published release, or a build from a recipe.
- Sources on Hugging Face, GitHub, Ollama, ModelScope, Civitai, Kaggle, NGC, CSGHub, OCI registries, git, mirrors, and local directories.
- A gateway that speaks the OpenAI, Anthropic, and Ollama APIs from any runtime, with per route limits, CORS, and TLS.
- A chat console and `nebu chat` for prompting whatever model is loaded in whatever runtime/runtime-slot, for immediate inference testing and debugging.
- Linux, macOS, Windows, and FreeBSD hosts, processes owned and found again on every one.

