<script lang="ts">
  import { live, taskFor, clock } from '$lib/state.svelte';
  import { dnd, acceptsModel } from '$lib/dnd.svelte';
  import { dropModelOnSlot, slotOccupied } from '$lib/launch';
  import { swapSlot, editSlot, evictSlot, deleteSlot } from '$lib/slotActions.svelte';
  import { bytes, enumLabel, count, duration } from '$lib/format';
  import { SlotState, type Slot } from '$proto/slot_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { RouteState } from '$proto/gateway_pb';
  import { ArrowLeftRight, LogOut, Pencil, Trash2, Activity, MessageSquare } from '@lucide/svelte';
  import Menu from './ui/Menu.svelte';
  import TaskChip from './TaskChip.svelte';

  // One slot as a bay: a state rail, what it serves, and a drop target for stored models
  let { slot, onOpen }: { slot: Slot; onOpen: (slot: Slot) => void } = $props();

  let over = $state(false);
  const instance = $derived(slot.instanceId ? live.instances.get(slot.instanceId) : undefined);
  const route = $derived(live.routes.get(slot.name));
  const occupied = $derived(slotOccupied(slot.id));
  const swapTask = $derived(taskFor('swap', { slot: slot.id }));
  const armed = $derived(!!dnd.model);
  const devices = $derived(slot.deviceIds.map((id) => live.host?.devices.find((d) => d.id === id)?.name ?? id));
  const phase = $derived(enumLabel(SlotState, slot.state));
  const rail: Record<string, string> = { ready: 'bg-ok', starting: 'bg-warn', swapping: 'bg-warn', draining: 'bg-warn', failed: 'bg-bad', empty: 'bg-line-strong' };
  const moving = $derived(['starting', 'swapping', 'draining'].includes(phase));
</script>

<div
  role="group"
  aria-label="slot {slot.name}"
  class="drop-target group relative flex items-stretch overflow-hidden rounded-lg border border-line bg-surface transition-colors hover:border-line-strong"
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
  <div class="w-1 shrink-0 {rail[phase] ?? 'bg-line-strong'} {moving ? 'animate-pulse' : ''}"></div>
  <div class="grid min-w-0 flex-1 grid-cols-[minmax(0,15rem)_minmax(0,1fr)_auto] items-center gap-x-5 px-4 py-3">
    <div class="min-w-0">
      <button class="block max-w-full truncate text-left font-mono text-sm font-semibold text-fg hover:text-accent" onclick={() => onOpen(slot)}>{slot.name}</button>
      <div class="mt-0.5 flex items-center gap-2 text-[11.5px]">
        <span class="capitalize {phase === 'failed' ? 'text-bad' : phase === 'ready' ? 'text-ok' : 'text-fg-muted'}">{phase}</span>
        {#if instance && occupied}<span class="text-fg-faint">up {duration(instance.readyAt ?? instance.createdAt, undefined, clock.now)}</span>{/if}
        {#if slot.description}<span class="truncate text-fg-faint" title={slot.description}>· {slot.description}</span>{/if}
      </div>
    </div>

    <div class="min-w-0">
      {#if over}
        <div class="flex h-10 items-center rounded-md border border-dashed border-accent/60 bg-accent/5 px-3 text-sm text-accent">{occupied ? 'Drop to swap' : 'Drop to run'}</div>
      {:else if slot.request?.repo}
        <div class="truncate font-mono text-xs text-fg" title={slot.request.repo}>{slot.request.repo}<span class="text-fg-muted"> · {slot.request.group}</span></div>
        <div class="mt-0.5 flex flex-wrap items-center gap-x-3 text-[11.5px] text-fg-faint">
          {#if instance}<span>{enumLabel(InstanceState, instance.state)}{instance.runtimeId ? ` on ${instance.runtimeId}` : ''}</span>{/if}
          <span class="truncate" title={devices.join(', ')}>{devices.length ? devices.join(', ') : 'all devices'}</span>
          {#if slot.memoryBytes}<span>{bytes(slot.memoryBytes, 0)} cap</span>{/if}
        </div>
        {#if swapTask}<div class="mt-1.5"><TaskChip task={swapTask} label="Swapping" /></div>{/if}
        {#if slot.error && slot.state === SlotState.FAILED}<div class="mt-1 truncate text-xs text-bad" title={slot.error}>{slot.error}</div>{/if}
      {:else}
        <div class="flex h-10 items-center rounded-md border border-dashed px-3 text-xs {armed ? 'border-accent/50 text-accent' : 'border-line text-fg-faint'}">{armed ? 'Drop to run' : 'Empty'}</div>
        <div class="mt-0.5 flex flex-wrap items-center gap-x-3 text-[11.5px] text-fg-faint">
          <span class="truncate">{devices.length ? devices.join(', ') : 'all devices'}</span>
          {#if slot.memoryBytes}<span>{bytes(slot.memoryBytes, 0)} cap</span>{/if}
        </div>
      {/if}
    </div>

    <div class="flex items-center gap-3">
      {#if route}
        <span class="hidden items-center gap-1 text-xs tabular-nums sm:flex {route.state === RouteState.READY ? 'text-ok' : 'text-fg-faint'}" title="requests through the gateway">
          <Activity size={12} />{count(route.requests)}{#if route.inFlight}<span class="text-fg-muted"> · {route.inFlight} live</span>{/if}
        </span>
      {/if}
      <Menu
        items={[
          { label: occupied ? 'Swap model' : 'Run a model', icon: ArrowLeftRight, onSelect: () => swapSlot(slot) },
          ...(route?.state === RouteState.READY ? [{ label: 'Chat', icon: MessageSquare, href: `/chat?model=${encodeURIComponent(slot.name)}` }] : []),
          { label: 'Edit', icon: Pencil, onSelect: () => editSlot(slot) },
          { label: 'Evict', icon: LogOut, onSelect: () => evictSlot(slot), disabled: !occupied },
          { label: '', separator: true },
          { label: 'Delete', icon: Trash2, tone: 'bad', onSelect: () => deleteSlot(slot) }
        ]}
      />
    </div>
  </div>
</div>
