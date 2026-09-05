<script lang="ts">
  import { live, taskFor, clock } from '$lib/state.svelte';
  import { slotOccupied } from '$lib/launch';
  import { instanceMemory } from '$lib/instances';
  import { swapSlot, editSlot, evictSlot, deleteSlot } from '$lib/slotActions.svelte';
  import { bytes, count, duration } from '$lib/format';
  import { SlotState, type Slot } from '$proto/slot_pb';
  import { RouteState } from '$proto/gateway_pb';
  import { ArrowLeftRight, LogOut, Pencil, Trash2, MessageSquare, Play, PanelRight } from '@lucide/svelte';
  import Button from './ui/Button.svelte';
  import Menu from './ui/Menu.svelte';
  import StatePill from './ui/StatePill.svelte';
  import TaskChip from './TaskChip.svelte';

  // One slot: its name, what it serves, how it is doing, and the one thing to do next
  let { slot, onOpen }: { slot: Slot; onOpen: (slot: Slot) => void } = $props();

  const instance = $derived(slot.instanceId ? live.instances.get(slot.instanceId) : undefined);
  const route = $derived(live.routes.get(slot.name));
  const occupied = $derived(slotOccupied(slot.id));
  const answering = $derived(route?.state === RouteState.READY);
  const swapTask = $derived(taskFor('swap', { slot: slot.id }));
  const failed = $derived(slot.state === SlotState.FAILED);
  const devices = $derived(slot.deviceIds.map((id) => live.host?.devices.find((d) => d.id === id)?.name ?? id));
  const memory = $derived(instance ? instanceMemory(instance) : '');
  const install = $derived(instance?.installId ? live.installs.get(instance.installId) : undefined);
</script>

<article class="card flex flex-col gap-4 p-5 transition-colors hover:border-line-strong {failed ? 'border-bad/40' : ''}">
  <div class="flex items-start gap-3">
    <button type="button" class="min-w-0 flex-1 text-left" onclick={() => onOpen(slot)}>
      <div class="truncate font-mono text-base font-semibold text-fg hover:text-accent">{slot.name}</div>
      {#if slot.description}<div class="truncate text-sm text-fg-muted">{slot.description}</div>{/if}
    </button>
    <StatePill values={SlotState} value={slot.state} />
  </div>

  <div class="min-h-[2.75rem]">
    {#if slot.request?.repo}
      <div class="truncate text-sm font-medium text-fg" title={slot.request.repo}>{slot.request.repo}</div>
      <div class="truncate text-sm text-fg-muted">
        <span class="font-mono">{slot.request.group}</span>
        {#if instance?.runtimeId}<span> on {instance.runtimeId}{install?.version ? ` ${install.version}` : ''}</span>{/if}
      </div>
    {:else}
      <div class="text-sm text-fg-muted">Nothing running</div>
      <div class="truncate text-sm text-fg-faint">{devices.length ? devices.join(', ') : 'Any device'}{slot.memoryBytes ? ` · ${bytes(slot.memoryBytes, 0)} cap` : ''}</div>
    {/if}
    {#if swapTask}<div class="mt-2"><TaskChip task={swapTask} label="Swapping" /></div>{/if}
    {#if failed && slot.error}<div class="mt-2 line-clamp-2 text-sm text-bad" title={slot.error}>{slot.error}</div>{/if}
  </div>

  {#if occupied && instance}
    <dl class="grid grid-cols-3 gap-3 rounded-lg bg-sunken/60 px-3 py-2.5">
      <div><dt class="text-xs text-fg-faint">Up</dt><dd class="text-sm tabular-nums text-fg">{duration(instance.readyAt ?? instance.createdAt, undefined, clock.now)}</dd></div>
      <div><dt class="text-xs text-fg-faint">Requests</dt><dd class="text-sm tabular-nums text-fg">{count(route?.requests ?? 0n)}{#if route?.inFlight}<span class="text-fg-muted">{' · '}{route.inFlight} live</span>{/if}</dd></div>
      <div><dt class="text-xs text-fg-faint">Memory</dt><dd class="text-sm tabular-nums text-fg">{memory || '–'}</dd></div>
    </dl>
  {/if}

  <div class="mt-auto flex items-center gap-2 border-t border-line pt-4">
    {#if occupied}
      {#if answering}<Button size="sm" icon={MessageSquare} href="/chat?model={encodeURIComponent(slot.name)}">Chat</Button>{/if}
      <Button size="sm" icon={ArrowLeftRight} onclick={() => swapSlot(slot)}>Swap</Button>
    {:else}
      <Button size="sm" variant="primary" icon={Play} onclick={() => swapSlot(slot)}>Run a model</Button>
    {/if}
    <span class="ml-auto">
      <Menu
        size="sm"
        items={[
          { label: 'Details', icon: PanelRight, onSelect: () => onOpen(slot) },
          { label: 'Edit', icon: Pencil, onSelect: () => editSlot(slot) },
          { label: 'Evict', icon: LogOut, onSelect: () => evictSlot(slot), disabled: !occupied },
          { label: '', separator: true },
          { label: 'Delete', icon: Trash2, tone: 'bad', onSelect: () => deleteSlot(slot) }
        ]}
      />
    </span>
  </div>
</article>
