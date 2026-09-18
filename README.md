# nebu
Nebu is a model loading shim for inference services like llama.cpp, vLLM, SGLang, NeMo, and stable-diffusion.cpp

# Intro

Nebu (named after Nebuchadnezzar, the matrix spaceship, not the babylonian guy) is yet another time-saving solution for a niche engineering problem - this time it's managing open source model weights and coordinating with backend inference platforms like `llama.cpp`. 

- Five runtimes: llama.cpp, vLLM, SGLang, and NeMo, and stable-diffusion.cpp
- Diffusers: Image and video generation
- Three runtime installers: Adopt binary, Download binary, Build Binary
- Model sources: Hugging Face, GitHub, Ollama, ModelScope, Civitai, Kaggle, NGC, CSGHub, OCI registries, git, mirrors, and local directories
- API Dialects: OpenAI, Anthropic, and Ollama
- Discord: bots that route chat, images, and video through the gateway, with personas, automations, and sharding
- Platforms: Linux, macOS, Windows, and FreeBSD

# Install

On Arch Linux:

```sh
yay -S nebu-bin # or nebu for full build
systemctl --user enable --now nebu.service
```

For everyone else:

Releases available for your platform at [GitHub Releases](https://github.com/nickheyer/nebu/releases)

# Discord

A bot is a Discord token plus everything about how it behaves, kept as a row like a slot and run by the daemon. Each bot has one or more personas: a name, a face, a system prompt, the language, image, and video routes it answers through, sampling, and how much of the channel it remembers. Every generation goes through the gateway in process, so it is traced, limited, and listed under Requests like any other client's.

1. Create an application at https://discord.com/developers/applications, open Bot, reset the token, and turn on the Message Content Intent (and the Server Members Intent when a bot listens for members joining).
2. Create the bot in the UI under Bots, or from the shell, then add it to a server with the invite link the bot page shows.

```sh
nebu bots create nova --token "$DISCORD_TOKEN" --model llama --image-model sdxl --system "You are Nova." --start
nebu bots show nova
nebu bots export nova > nova.yaml    # every setting, edit and pass back
nebu bots update nova --spec nova.yaml
nebu bots say nova 123456789012345678 "a lighthouse at dusk" --kind image
nebu bots activity nova --follow
```

What a bot can do, all of it per persona where it makes sense:

- Answer mentions, replies, direct messages, and wake words, or every message in chosen channels, with allow and deny lists for guilds, channels, and people, cooldowns, and a cap on bots answering bots
- Slash commands `/ask`, `/imagine`, `/video`, `/persona`, `/models`, `/reset`, and `/help`, each renameable or left out, and the same words behind a prefix such as `!`
- Images and video in and out: attached images reach vision models, attached videos are sampled into frames with ffmpeg, and `/imagine` and `/video` start from an attached image or video
- Several personas per bot, each speaking through a channel webhook with its own name and avatar so they read as different people, chosen per channel with `/persona`
- Pass for a person: a pause before typing, typing at a set speed with the indicator up, long answers split into several messages, lowercase casual style, emoji reactions by chance, a chance to answer unaddressed messages and a chance to ignore addressed ones, and hours the persona is awake in its own timezone
- Automations: cron or interval schedules, keywords by regular expression, mentions, member joins, reactions, and prefix commands, each carrying out a chat, image, or video generation from a template, a fixed text, a reaction, or a presence change
- Sharding: the shard count Discord recommends, a fixed count, or a subset of shard ids so one bot spreads over several hosts
- Presence: status and a rotating activity line

`discord.ffmpeg` in the config names the ffmpeg binary used to sample video attachments; it is found on PATH when unset.

## Build

Requires Go 1.27, docker, and make.

```sh
make gen
make build
```

See `Makefile` for all commands, there are many.

