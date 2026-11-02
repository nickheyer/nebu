<script lang="ts">
  import { live, nodeProfile, orderedSlots, hostGpus, type DeviceGroup } from '$lib/state.svelte';
  import { bytes } from '$lib/format';
  import type { Device } from '$proto/host_pb';
  import type { Snippet } from 'svelte';
  import { hostItems } from './Devices.svelte';
  import SizeBar from './ui/SizeBar.svelte';
  import Chip from './ui/Chip.svelte';

  // Accelerator rows with the memory each host reports used on them and a slot's cap line
  let {
    groups,
    cap = 0n,
    exceptSlot = '',
    lead,
    rowClass = ''
  }: {
    groups?: DeviceGroup[];
    cap?: bigint;
    // The slot whose own pin is left out of the pins listed on each device
    exceptSlot?: string;
    // Rendered before each device's name, such as a checkbox
    lead?: Snippet<[Device, DeviceGroup]>;
    rowClass?: string;
  } = $props();

  const shown = $derived<DeviceGroup[]>(groups ?? [{ nodeId: '', node: '', self: true, ready: true, devices: hostGpus() }]);

  const displayId = (d: Device, g: DeviceGroup) => (g.nodeId && d.id.startsWith(`${g.nodeId}/`) ? d.id.slice(g.nodeId.length + 1) : d.id);
  const profileOf = (g: DeviceGroup) => (g.self ? live.host : nodeProfile(g.nodeId));
  function poolOf(d: Device, g: DeviceGroup) {
    const bare = displayId(d, g);
    return profileOf(g)?.pools.find((p) => p.deviceId === bare || p.id === bare);
  }
  function overlays(d: Device, g: DeviceGroup) {
    const pool = poolOf(d, g);
    return [{ start: 'left' as const, items: hostItems(pool?.totalBytes ?? d.memoryTotalBytes, pool?.freeBytes ?? d.memoryFreeBytes) }];
  }
  function pins(d: Device, g: DeviceGroup): string[] {
    if (!g.self) return [];
    return orderedSlots()
      .filter((s) => s.id !== exceptSlot && s.deviceIds.includes(d.id))
      .map((s) => s.name);
  }
</script>

<div class="divide-y divide-line overflow-hidden rounded-md border border-line bg-sunken/40">
  {#each shown as g (g.nodeId)}
    {#if g.node}
      <div class="flex items-center gap-2 bg-sunken/60 px-3 py-2 text-xs font-medium text-fg-muted">
        <span class="min-w-0 flex-1 truncate">{g.node}</span>
        {#if !g.ready}<span class="shrink-0 text-warn">Unreachable</span>{/if}
        <span class="shrink-0 font-normal text-fg-faint">{g.devices.length} {g.devices.length === 1 ? 'GPU' : 'GPUs'}</span>
      </div>
    {/if}
    {#each g.devices as d (d.id)}
      {@const total = poolOf(d, g)?.totalBytes ?? d.memoryTotalBytes}
      {@const pinned = pins(d, g)}
      <div class="flex flex-col gap-1.5 px-3 py-2.5 {g.node ? 'pl-6' : ''} {rowClass}">
        <div class="flex min-w-0 items-center gap-3">
          {#if lead}{@render lead(d, g)}{/if}
          <span class="min-w-0 flex-1 truncate text-sm text-fg" title={d.name || displayId(d, g)}>{d.name || displayId(d, g)}</span>
          {#if d.name}<span class="max-w-24 shrink-0 truncate font-mono text-xs text-fg-faint" title={d.id}>{displayId(d, g)}</span>{/if}
          {#each pinned as name (name)}<Chip text={name} title="Slot {name} also pins this GPU" />{/each}
          <span class="w-16 shrink-0 text-right text-xs tabular-nums text-fg-muted">{bytes(total, 0)}</span>
        </div>
        {#if total > 0n}
          <SizeBar total={total} overlays={overlays(d, g)} free="free" marks={cap > 0n && cap < total ? [{ at: cap, label: 'cap' }] : []} />
        {/if}
      </div>
    {/each}
  {/each}
</div>
