# nebu
Nebu is a model loading shim for inference services like llama.cpp, vLLM, SGLang, and NeMo

# Intro

Nebu (named after Nebuchadnezzar, the matrix spaceship, not the babylonian guy) is yet another time-saving solution for a niche engineering problem - this time it's managing open source model weights and coordinating with backend inference platforms like `llama.cpp`. "Like" is doing real work in that sentence: every runtime is a manifest, a recipe, and triage rules under `spec/`, no Go names one, and the same interface should jerry-rig anything.

What ships today, each with its own page under `docs/`:

- Four runtimes, llama.cpp, vLLM, SGLang, and NeMo, each installed by a method its manifest lists, a binary on the host, a published release, or a build from a recipe, chosen and configured by you.
- Sources on Hugging Face, GitHub, Ollama, ModelScope, Civitai, Kaggle, NGC, CSGHub, OCI registries, git, mirrors, and local directories, all merged in one catalog.
- A gateway that speaks the OpenAI, Anthropic, and Ollama APIs from any runtime, with per route limits, CORS, and TLS.
- A chat page and `nebu chat` for prompting whatever is loaded through the gateway itself.
- Linux, macOS, Windows, and FreeBSD hosts, processes owned and found again on every one.

We are just doing what `ollama` already does, minus the weird commercial features, and with the added performance gained through better code, less bloat and coupling, and a generic patch system. Nebu will *not* be an inference provider, you still go through your router or direct api, we just facilitate the mundane devops of downloading models, moving them around the filesystem, stopping and starting the runetime(s), and if everything works out up to this point - installing/patching open source backend services to keep up with the new bleeding-edge ai standards and features that have to be rapidly adopted every week... 

The end goal objectives are: 

1. Jr. engineer can scroll through a *arr-like catalogue of open source models from hf/elsewhere from within a hosted web ui, see that there is a new model with some over exaggerated benchmark that requires 120% of their gpu's available vram, click a "install" button to pull down the files to a managed file store and get it provisioned for hot swapping, then user can drag or click the model in the store to the currently running model in the "slot"(s) to swap (or insert if open slot), auto reload whatever needs to be reloaded (maybe defined in some provider profile store?) on change of slotted model if reloading is required (probably is), then hit the same api they were just hitting with the old model, but now they are talking to the new one!

2. Same deal as #1 mostly. Catalogue of backend providers like llama.cpp, each have a mapped implementation to some generic interface we define. We can configure those providers at install time with all the knobs they'd normally provide. We can also define slot numbers, limiters, labels, the works! Should also be able to perform configurations "dynamically" (or statically if dynamic not possible or chosen) based on host resources (or limits imposed on those resources).

3. Provide quantifiable resource requirements of models (or quants of models) in real time, in direct comparison to host resources without all the elitist redditors going "oh you need at least 6 Nvidia H200's to even think about running this". This should be a natural intuitive side effect of #1 and #2 without needing to be some slop feature afterthought.

4. Be a time save, not a time suck.
