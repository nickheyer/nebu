<script lang="ts">
  import { live, instancesOnPool, orderedSlots, hostGpus, type DeviceGroup } from '$lib/state.svelte';
  import { bytes } from '$lib/format';
  import type { Device } from '$proto/host_pb';
  import type { Snippet } from 'svelte';
  import SizeBar from './ui/SizeBar.svelte';
  import Chip from './ui/Chip.svelte';

  // Accelerator rows with other use, instance use, the highlighted instance's share, and a slot's cap line
  let {
    groups,
    cap = 0n,
    instance = '',
    highlight = 'this slot',
    exceptSlot = '',
    lead,
    rowClass = ''
  }: {
    groups?: DeviceGroup[];
    cap?: bigint;
    // The instance whose planned share is drawn apart from the other instances
    instance?: string;
    highlight?: string;
    // The slot whose own pin is left out of the pins listed on each device
    exceptSlot?: string;
    // Rendered before each device's name, such as a checkbox
    lead?: Snippet<[Device, DeviceGroup]>;
    rowClass?: string;
  } = $props();

  const shown = $derived<DeviceGroup[]>(groups ?? [{ nodeId: '', node: '', self: true, ready: true, devices: hostGpus() }]);

  function poolOf(d: Device, g: DeviceGroup) {
    return g.self ? live.host?.pools.find((p) => p.deviceId === d.id) : undefined;
  }
  function overlays(d: Device, g: DeviceGroup) {
    const pool = poolOf(d, g);
    const total = pool?.totalBytes ?? d.memoryTotalBytes;
    const free = pool?.freeBytes ?? d.memoryFreeBytes;
    const inUse = total > free ? total - free : 0n;
    const ours = pool ? instancesOnPool(pool.id) : 0n;
    const mine = pool && instance ? (live.instances.get(instance)?.plan?.pools.filter((p) => p.poolId === pool.id).reduce((a, p) => a + p.usedBytes, 0n) ?? 0n) : 0n;
    const rest = ours > mine ? ours - mine : 0n;
    const others = inUse > ours ? inUse - ours : 0n;
    const items = [];
    if (others > 0n) items.push({ label: 'other', size: others, tone: 'neutral' as const });
    if (rest > 0n) items.push({ label: 'instances', size: rest, tone: 'info' as const });
    if (mine > 0n) items.push({ label: highlight, size: mine, tone: 'accent' as const });
    return [{ start: 'left' as const, items }];
  }
  function pins(d: Device, g: DeviceGroup): string[] {
    if (!g.self) return [];
    return orderedSlots()
      .filter((s) => s.id !== exceptSlot && s.deviceIds.includes(d.id))
      .map((s) => s.name);
  }
  const displayId = (d: Device, g: DeviceGroup) => (g.nodeId && d.id.startsWith(`${g.nodeId}/`) ? d.id.slice(g.nodeId.length + 1) : d.id);
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
