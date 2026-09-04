# Slots and swaps

A slot is a reservation of devices and a memory budget under one public name that never
changes. Instances come and go inside it.

```
nebu slots create main --device GPU-1234 --memory 20GiB --runtime llamacpp
nebu run org/model --group Q4_K_M --slot main
nebu swap main org/other --group Q8_0
```

Planning inside a slot sees only the slot's device pools, each capped at the budget, so a
second slot on the same card plans around the first. The cap is per device rather than a total
across the slot's devices: runtimes allocate on each card separately, so a per device cap is
what the driver enforces, while a total could be met with one card overflowing. The manifest's launch env pins the process to
the slot's devices through `.devices`: the shipped llama.cpp manifest sets
`CUDA_VISIBLE_DEVICES` to the slot's NVIDIA device ids, `ROCR_VISIBLE_DEVICES` to its AMD card
numbers, and `GGML_VK_VISIBLE_DEVICES` to their indices for the Vulkan build, and every one of
them renders empty and is dropped without a slot. The slot's default runtime and params apply
under whatever the run passes and over the runtime's profile, so a slot narrows a profile and a
run narrows a slot. A slot also carries the request limits its route enforces at the gateway,
in flight cap, rate and burst, request and upstream timeouts, each inheriting `gateway.policy`
where it is zero, see [gateway.md](gateway.md). `nebu slots update` writes new limits to the
route at once, no swap needed, while devices, budget, runtime, and params apply on the next run.

The gateway route for a slot is its name. While the slot is empty or starting the route
stays in place and answers `503` with `Retry-After`, never `404`, so a client that retries
keeps working across a swap.

## Swap

`nebu swap` plans the new model with the old one still running.

- When the plan fits, the new instance starts beside the old one. Once it is healthy the
  route flips to it, the old instance drains, meaning it takes no new requests and waits
  up to `gateway.drain_timeout_ms` for in flight ones, and then stops. Nothing is refused.
- When it does not fit, or `--drain-first` is passed, the old instance drains and stops
  first, the route goes pending, and the new instance starts. If the new one fails, the old
  request is replayed so the slot serves what it served before and the error is kept on the
  slot.

`nebu slots evict` drains and stops the occupant and leaves the name pending. `nebu slots
remove` is refused while the slot serves or while a watch or want swaps into it; `--force`
stops the occupant, drops those swaps, and removes the name. A plain `nebu run` cannot take a
slot's name, and a failed swap leaves the pending route with the limits the slot has now.

## Restart

Slots are rows in the store. On start the daemon adopts runtimes that survived, marks the
rest, relaunches every instance that was never stopped by request, and each slot picks up
the instance bound to it and routes it as soon as it answers health.
