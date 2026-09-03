# nebu
Nebu is a model loading shim for inference services like llama.cpp, vllm, and others

# Intro

Nebu (named after Nebuchadnezzar, the matrix spaceship, not the babylonian guy) is yet another time-saving solution for a niche engineering problem - this time it's managing open source model weights and coordinating with backend inference platforms like `llama.cpp`. I say "like" but this is really my only target right now, though should be generic enough of an interface that we can jerry-rig anything, so if you only see llama cpp when this is public, you'll know I failed to keep things generic.

We are just doing what `ollama` already does, minus the weird commercial features, and with the added performance gained through better code, less bloat and coupling, and a generic patch system. Nebu will *not* be an inference provider, you still go through your router or direct api, we just facilitate the mundane devops of downloading models, moving them around the filesystem, stopping and starting the runetime(s), and if everything works out up to this point - installing/patching open source backend services to keep up with the new bleeding-edge ai standards and features that have to be rapidly adopted every week... 

The end goal objectives are: 

1. Jr. engineer can scroll through a *arr-like catalogue of open source models from hf/elsewhere from within a hosted web ui, see that there is a new model with some over exaggerated benchmark that requires 120% of their gpu's available vram, click a "install" button to pull down the files to a managed file store and get it provisioned for hot swapping, then user can drag or click the model in the store to the currently running model in the "slot"(s) to swap (or insert if open slot), auto reload whatever needs to be reloaded (maybe defined in some provider profile store?) on change of slotted model if reloading is required (probably is), then hit the same api they were just hitting with the old model, but now they are talking to the new one!

2. Same deal as #1 mostly. Catalogue of backend providers like llama.cpp, each have a mapped implementation to some generic interface we define. We can configure those providers at install time with all the knobs they'd normally provide. We can also define slot numbers, limiters, labels, the works! Should also be able to perform configurations "dynamically" (or statically if dynamic not possible or chosen) based on host resources (or limits imposed on those resources).

3. Provide quantifiable resource requirements of models (or quants of models) in real time, in direct comparison to host resources without all the elitist redditors going "oh you need at least 6 Nvidia H200's to even think about running this". This should be a natural intuitive side effect of #1 and #2 without needing to be some slop feature afterthought.

4. Be a time save, not a time suck.

> DISCLAIMER: I don't even know what language I'm going to use at the time of writing this... not Python, probably not Go since ollama uses it and it seems like they regret it, I haven't written C++ since college, Rust seems like it would have the same hangups as go, I just don't know. 