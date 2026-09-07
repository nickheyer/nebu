<script lang="ts">
  import { live, taskFor, clock, deviceName, groupLabel } from '$lib/state.svelte';
  import { slotOccupied } from '$lib/launch';
  import { instanceMemory } from '$lib/instances';
  import { swapSlot, evictSlot, deleteSlot } from '$lib/slotActions.svelte';
  import { bytes, count, duration, enumLabel, tone } from '$lib/format';
  import { SlotState, type Slot } from '$proto/slot_pb';
  import { RouteState } from '$proto/gateway_pb';
  import { ArrowLeftRight, LogOut, Pencil, Trash2, MessageSquare, Play, PanelRight } from '@lucide/svelte';
  import Button from './ui/Button.svelte';
  import IconButton from './ui/IconButton.svelte';
  import Menu from './ui/Menu.svelte';
  import State from './ui/State.svelte';
  import TaskChip from './TaskChip.svelte';

  // One slot as a bay: its name and state, what it serves, its numbers, and what to do next
  let { slot, onOpen }: { slot: Slot; onOpen: (slot: Slot, tab?: string) => void } = $props();

  const instance = $derived(slot.instanceId ? live.instances.get(slot.instanceId) : undefined);
  const route = $derived(live.routes.get(slot.name));
  const occupied = $derived(slotOccupied(slot.id));
  const answering = $derived(route?.state === RouteState.READY);
  const swapTask = $derived(taskFor('swap', { slot: slot.id }));
  const failed = $derived(slot.state === SlotState.FAILED);
  const devices = $derived(slot.deviceIds.map(deviceName));
  const memory = $derived(instance ? instanceMemory(instance) : '');
  const install = $derived(instance?.installId ? live.installs.get(instance.installId) : undefined);
  const rails: Record<string, string> = { ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-bad', neutral: 'bg-line-strong' };
  const rail = $derived(rails[tone(enumLabel(SlotState, slot.state))] ?? rails.neutral);
</script>

<article class="bay grid grid-cols-[minmax(8rem,12rem)_minmax(0,1fr)_auto] items-center gap-x-6 py-3 pr-2 pl-4">
  <span class="absolute inset-y-2.5 left-0 w-0.5 rounded-full {rail}"></span>
  <button type="button" class="min-w-0 text-left" onclick={() => onOpen(slot)}>
    <div class="truncate font-mono text-sm font-semibold text-fg">{slot.name}</div>
    <div class="mt-1 flex items-center gap-2">
      <State values={SlotState} value={slot.state} />
      {#if occupied && instance}<span class="text-xs tabular-nums text-fg-faint">{duration(instance.readyAt ?? instance.createdAt, undefined, clock.now)}</span>{/if}
    </div>
  </button>
  <button type="button" class="min-w-0 text-left" onclick={() => onOpen(slot)}>
    {#if slot.request?.repo}
      <div class="truncate text-sm text-fg" title={slot.request.repo}>{slot.request.repo}</div>
      <div class="mt-1 truncate text-xs text-fg-muted">
        <span class="font-mono">{groupLabel(slot.request)}</span>
        {#if instance?.runtimeId}<span>{' · '}{instance.runtimeId}{install?.version ? ` ${install.version}` : ''}</span>{/if}
      </div>
    {:else}
      <div class="text-sm text-fg-muted">Empty</div>
      <div class="mt-1 truncate text-xs text-fg-faint">{devices.length ? devices.join(', ') : 'Any device'}{slot.memoryBytes ? ` · ${bytes(slot.memoryBytes, 0)} cap` : ''}</div>
    {/if}
    {#if swapTask}<div class="mt-1.5"><TaskChip task={swapTask} label="Swapping" /></div>{/if}
    {#if failed && slot.error}<div class="mt-1 truncate text-xs text-bad" title={slot.error}>{slot.error}</div>{/if}
  </button>
  <div class="flex items-center gap-5">
    {#if occupied && instance}
      <dl class="hidden items-center gap-5 text-right lg:flex">
        <div><dt class="caps text-fg-faint">Requests</dt><dd class="text-sm tabular-nums text-fg">{count(route?.requests ?? 0n)}{#if route?.inFlight}<span class="text-fg-faint"> · {route.inFlight} live</span>{/if}</dd></div>
        <div><dt class="caps text-fg-faint">Memory</dt><dd class="text-sm tabular-nums text-fg">{memory || '–'}</dd></div>
      </dl>
    {/if}
    <div class="flex items-center gap-1">
      {#if occupied}
        {#if answering}<Button size="sm" variant="subtle" icon={MessageSquare} href="/chat?model={encodeURIComponent(slot.name)}">Chat</Button>{/if}
        <Button size="sm" variant="subtle" icon={ArrowLeftRight} onclick={() => swapSlot(slot)}>Swap</Button>
      {:else}
        <Button size="sm" variant="primary" icon={Play} onclick={() => swapSlot(slot)}>Run</Button>
      {/if}
      <IconButton size="sm" icon={Pencil} label="Settings" onclick={() => onOpen(slot, 'settings')} />
      <Menu
        size="sm"
        items={[
          { label: 'Details', icon: PanelRight, onSelect: () => onOpen(slot) },
          { label: 'Evict', icon: LogOut, onSelect: () => evictSlot(slot), disabled: !occupied },
          { label: '', separator: true },
          { label: 'Delete', icon: Trash2, tone: 'bad', onSelect: () => deleteSlot(slot) }
        ]}
      />
    </div>
  </div>
</article>
