<script lang="ts">
  import { goto } from '$app/navigation';
  import { live, taskFor, clock, deviceName, groupLabel, runtimeName } from '$lib/state.svelte';
  import { slotOccupied } from '$lib/launch';
  import { instanceMemory } from '$lib/instances';
  import { swapSlot, evictSlot, deleteSlot, relaunchSlot } from '$lib/actions.svelte';
  import { bytes, count, duration, enumLabel, tone } from '$lib/format';
  import { SlotState, type Slot } from '$proto/slot_pb';
  import { RouteState } from '$proto/gateway_pb';
  import { ArrowLeftRight, LogOut, Settings2, Trash2, MessageSquare, Play, RotateCcw, PanelRight } from '@lucide/svelte';
  import Button from './ui/Button.svelte';
  import Menu from './ui/Menu.svelte';
  import State from './ui/State.svelte';
  import TaskChip from './TaskChip.svelte';

  // One slot: its number and name, what it serves, its figures, and what to do next
  let { slot }: { slot: Slot } = $props();

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
  const href = $derived(`/slots/${slot.id}`);
</script>

<article class="card card-link relative grid grid-cols-[2.25rem_minmax(0,1fr)_auto] items-center gap-x-4 py-3 pr-3 pl-3">
  <span class="absolute inset-y-3 left-0 w-0.5 rounded-full {rail}"></span>
  <a {href} class="flex h-9 w-9 items-center justify-center rounded-md bg-raised font-mono text-sm font-semibold text-fg-muted" aria-label="Open slot {slot.name}">{slot.position}</a>
  <a {href} class="grid min-w-0 grid-cols-1 gap-x-6 gap-y-1 sm:grid-cols-[minmax(8rem,14rem)_minmax(0,1fr)]">
    <div class="min-w-0">
      <div class="truncate font-mono text-sm font-semibold text-fg">{slot.name}</div>
      <div class="mt-1 flex items-center gap-2">
        <State values={SlotState} value={slot.state} />
        {#if occupied && instance}<span class="text-xs tabular-nums text-fg-faint">{duration(instance.readyAt ?? instance.createdAt, undefined, clock.now)}</span>{/if}
      </div>
    </div>
    <div class="min-w-0">
      {#if slot.request?.repo}
        <div class="truncate text-sm text-fg" title={slot.request.repo}>{slot.request.repo}</div>
        <div class="mt-1 flex flex-wrap gap-x-3 truncate text-xs text-fg-muted">
          <span class="font-mono">{groupLabel(slot.request)}</span>
          {#if instance?.runtimeId}<span>{runtimeName(instance.runtimeId)}{install?.version ? ` ${install.version}` : ''}</span>{/if}
          {#if devices.length}<span>{devices.join(', ')}</span>{/if}
        </div>
      {:else}
        <div class="text-sm text-fg-muted">Empty</div>
        <div class="mt-1 truncate text-xs text-fg-faint">{devices.length ? devices.join(', ') : 'Any device'}{slot.memoryBytes ? ` · ${bytes(slot.memoryBytes, 0)} cap` : ''}{slot.runtimeId ? ` · ${runtimeName(slot.runtimeId)}` : ''}</div>
      {/if}
      {#if swapTask}<div class="mt-1.5"><TaskChip task={swapTask} label="Swapping" /></div>{/if}
      {#if failed && slot.error}<div class="mt-1 truncate text-xs text-bad" title={slot.error}>{slot.error}</div>{/if}
    </div>
  </a>
  <div class="flex items-center gap-4">
    {#if occupied && instance}
      <dl class="hidden items-center gap-5 text-right xl:flex">
        <div class="stat"><dt>Requests</dt><dd>{count(route?.requests ?? 0n)}{#if route?.inFlight}<span class="text-fg-faint"> · {route.inFlight} live</span>{/if}</dd></div>
        <div class="stat"><dt>Memory</dt><dd>{memory || '–'}</dd></div>
      </dl>
    {/if}
    <div class="flex items-center gap-1">
      {#if answering}<Button size="sm" variant="subtle" icon={MessageSquare} href="/chat?model={encodeURIComponent(slot.name)}">Chat</Button>{/if}
      {#if failed && slot.request}
        <Button size="sm" variant="primary" icon={RotateCcw} onclick={() => relaunchSlot(slot)}>Relaunch</Button>
      {:else if occupied}
        <Button size="sm" variant="subtle" icon={ArrowLeftRight} onclick={() => swapSlot(slot)}>Swap</Button>
      {:else}
        <Button size="sm" variant="primary" icon={Play} onclick={() => swapSlot(slot)}>Run</Button>
      {/if}
      <Menu
        size="sm"
        items={[
          { label: 'Open', icon: PanelRight, onSelect: () => goto(href) },
          { label: 'Settings', icon: Settings2, onSelect: () => goto(href + '?tab=settings') },
          { label: 'Evict', icon: LogOut, onSelect: () => evictSlot(slot), disabled: !occupied && !slot.request },
          { label: '', separator: true },
          { label: 'Delete', icon: Trash2, tone: 'bad', onSelect: () => deleteSlot(slot) }
        ]}
      />
    </div>
  </div>
</article>
