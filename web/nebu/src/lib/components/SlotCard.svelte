<script lang="ts">
  import { live, taskFor } from '$lib/state.svelte';
  import { dnd, acceptsModel } from '$lib/dnd.svelte';
  import { dropModelOnSlot, slotOccupied } from '$lib/launch';
  import { swapSlot, editSlot, evictSlot, deleteSlot } from '$lib/slotActions.svelte';
  import { bytes, enumLabel, count } from '$lib/format';
  import { SlotState, type Slot } from '$proto/slot_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { RouteState } from '$proto/gateway_pb';
  import { ArrowLeftRight, Box, LogOut, Pencil, Trash2, Activity } from '@lucide/svelte';
  import StateBadge from './ui/StateBadge.svelte';
  import Menu from './ui/Menu.svelte';
  import TaskChip from './TaskChip.svelte';

  let { slot, onOpen }: { slot: Slot; onOpen: (slot: Slot) => void } = $props();

  let over = $state(false);
  const instance = $derived(slot.instanceId ? live.instances.get(slot.instanceId) : undefined);
  const route = $derived(live.routes.get(slot.name));
  const occupied = $derived(slotOccupied(slot.id));
  const swapTask = $derived(taskFor('swap', { slot: slot.id }));
  const armed = $derived(!!dnd.model);
  const devices = $derived(slot.deviceIds.length ? slot.deviceIds.map((id) => live.host?.devices.find((d) => d.id === id)?.name ?? id) : []);
</script>

<div
  role="group"
  aria-label="slot {slot.name}"
  class="drop-target panel group relative flex flex-col gap-3 p-4 hover:border-line-strong"
  data-armed={armed}
  data-over={over}
  ondragover={(e) => {
    if (!acceptsModel(e)) return;
    e.preventDefault();
    over = true;
  }}
  ondragleave={() => (over = false)}
  ondrop={(e) => {
    over = false;
    dropModelOnSlot(e, slot.id);
  }}
>
  <div class="flex items-start gap-2">
    <div class="min-w-0 flex-1">
      <div class="flex items-center gap-2">
        <button class="truncate text-left text-sm font-semibold text-fg before:absolute before:inset-0 before:rounded-xl focus-visible:outline-none focus-visible:before:ring-2 focus-visible:before:ring-accent/60" onclick={() => onOpen(slot)}>{slot.name}</button>
        <StateBadge values={SlotState} value={slot.state} size="xs" />
      </div>
      {#if slot.description}<div class="mt-0.5 truncate text-xs text-fg-faint">{slot.description}</div>{/if}
    </div>
    <div class="relative z-10">
      <Menu
        items={[
          { label: occupied ? 'Swap model' : 'Run a model', icon: ArrowLeftRight, onSelect: () => swapSlot(slot) },
          { label: 'Edit slot', icon: Pencil, onSelect: () => editSlot(slot) },
          { label: 'Evict occupant', icon: LogOut, onSelect: () => evictSlot(slot), disabled: !occupied },
          { label: '', separator: true },
          { label: 'Delete slot', icon: Trash2, tone: 'bad', onSelect: () => deleteSlot(slot) }
        ]}
      />
    </div>
  </div>

  {#if over}
    <div class="flex h-[4.25rem] items-center justify-center rounded-md border border-dashed border-accent/60 bg-accent/5 text-sm text-accent">
      {occupied ? 'Drop to swap what this slot serves' : 'Drop to run here'}
    </div>
  {:else if slot.request?.repo}
    <div class="rounded-md border border-line bg-sunken px-3 py-2">
      <div class="flex items-center gap-2">
        <Box size={13} class="shrink-0 text-fg-faint" />
        <span class="truncate font-mono text-xs text-fg" title={slot.request.repo}>{slot.request.repo}</span>
      </div>
      <div class="mt-1 flex items-center justify-between gap-2 pl-[21px] text-[11.5px]">
        <span class="truncate font-mono text-fg-muted">{slot.request.group}</span>
        {#if instance}
          <span class="shrink-0 {occupied ? 'text-fg-muted' : 'text-fg-faint'}">
            {enumLabel(InstanceState, instance.state)}{instance.runtimeId ? ' · ' + instance.runtimeId : ''}
          </span>
        {/if}
      </div>
    </div>
  {:else}
    <div class="flex h-[4.25rem] items-center justify-center rounded-md border border-dashed border-line text-xs text-fg-faint {armed ? 'border-accent/50 text-accent' : ''}">
      {armed ? 'Drop here to run' : 'Empty, drag a stored model here'}
    </div>
  {/if}

  {#if swapTask}
    <div class="relative z-10"><TaskChip task={swapTask} label="Swapping" /></div>
  {/if}
  {#if slot.error && slot.state === SlotState.FAILED}
    <div class="truncate text-xs text-bad" title={slot.error}>{slot.error}</div>
  {/if}

  <div class="mt-auto flex flex-wrap items-center gap-x-3 gap-y-1 text-[11.5px] text-fg-faint">
    <span class="truncate" title={devices.join(', ')}>{devices.length ? devices.join(', ') : 'all devices'}</span>
    <span>·</span>
    <span>{slot.memoryBytes ? bytes(slot.memoryBytes, 0) + ' budget' : 'no budget cap'}</span>
    {#if route}
      <span class="ml-auto flex items-center gap-1 tabular-nums {route.state === RouteState.READY ? 'text-ok' : ''}">
        <Activity size={11} />
        {count(route.requests)} req{#if route.inFlight}<span> · {route.inFlight} live</span>{/if}
      </span>
    {/if}
  </div>
</div>
