<script lang="ts">
  import { api, message } from '$lib/api';
  import { live } from '$lib/state.svelte';
  import { enumName, human } from '$lib/format';
  import { SlotState, type Slot } from '$proto/slot_pb';
  import { InstanceState } from '$proto/instance_pb';
  import Badge from './Badge.svelte';

  let { slot, onDropModel, onSelect }: { slot: Slot; onDropModel?: (slotId: string, key: string) => void; onSelect?: (slot: Slot) => void } = $props();
  let over = $state(false);
  let error = $state('');

  const instance = $derived(slot.instanceId ? live.instances.get(slot.instanceId) : undefined);
  const route = $derived(live.routes.get(slot.name));

  function drop(ev: DragEvent) {
    ev.preventDefault();
    over = false;
    const key = ev.dataTransfer?.getData('text/nebu-model');
    if (key && onDropModel) onDropModel(slot.id, key);
  }

  async function evict() {
    error = '';
    try {
      await api.slots.evictSlot({ id: slot.id });
    } catch (err) {
      error = message(err);
    }
  }
</script>

<div
  role="group"
  class="card flex flex-col gap-2 transition {over ? 'border-emerald-500 bg-emerald-950/30' : ''}"
  ondragover={(e) => {
    e.preventDefault();
    over = true;
  }}
  ondragleave={() => (over = false)}
  ondrop={drop}
>
  <div class="flex items-center gap-2">
    <button class="text-left font-semibold hover:underline" onclick={() => onSelect?.(slot)}>{slot.name}</button>
    <Badge state={enumName(SlotState, slot.state)} />
    {#if route}<span class="muted text-xs">{route.requests.toString()} req</span>{/if}
  </div>
  <div class="text-sm">
    {#if slot.request}
      <div class="mono">{slot.request.repo} <span class="muted">{slot.request.group}</span></div>
    {:else}
      <div class="muted">drop a stored model here</div>
    {/if}
    {#if instance}
      <div class="muted text-xs">{enumName(InstanceState, instance.state)} on {instance.runtimeId} pid {instance.pid} {instance.endpoint}</div>
    {/if}
  </div>
  <div class="muted text-xs">
    {slot.deviceIds.length ? slot.deviceIds.join(', ') : 'all devices'}{slot.memoryBytes ? ', ' + human(slot.memoryBytes) + ' budget' : ''}
  </div>
  {#if slot.error}<div class="text-xs text-red-300">{slot.error}</div>{/if}
  {#if error}<div class="text-xs text-red-300">{error}</div>{/if}
  <div class="mt-auto flex gap-2">
    {#if slot.instanceId}
      <button class="btn" onclick={evict}>evict</button>
    {/if}
  </div>
</div>
