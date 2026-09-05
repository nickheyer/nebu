<script lang="ts">
  import { bytes, pct, enumLabel } from '$lib/format';
  import { DeviceKind, type Device, type MemoryPool } from '$proto/host_pb';
  import { Cpu, Microchip } from '@lucide/svelte';
  import Meter from './ui/Meter.svelte';

  let { device, pool }: { device: Device; pool?: MemoryPool } = $props();

  const total = $derived(pool?.totalBytes ?? device.memoryTotalBytes);
  const free = $derived(pool?.freeBytes ?? device.memoryFreeBytes);
  const used = $derived(total > free ? total - free : 0n);
  const p = $derived(pct(used, total));
  const Icon = $derived(device.kind === DeviceKind.CPU ? Cpu : Microchip);
</script>

<div class="panel flex flex-col gap-3 p-4">
  <div class="flex items-start gap-3">
    <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-line bg-raised text-fg-muted">
      <Icon size={15} />
    </div>
    <div class="min-w-0 flex-1">
      <div class="truncate text-sm font-medium text-fg" title={device.name}>{device.name || device.id}</div>
      <div class="truncate text-xs text-fg-faint">{device.vendor} {enumLabel(DeviceKind, device.kind)} · <span class="font-mono">{device.id}</span></div>
    </div>
    {#if total > 0n}
      <div class="text-right">
        <div class="text-sm font-semibold tabular-nums text-fg">{p.toFixed(0)}%</div>
        <div class="text-[11px] text-fg-faint">used</div>
      </div>
    {/if}
  </div>
  {#if total > 0n}
    <Meter value={used} max={total} height="lg" />
    <div class="flex items-center justify-between text-xs tabular-nums">
      <span class="text-fg-muted">{bytes(used)} used</span>
      <span class="text-fg-faint">{bytes(free)} free of {bytes(total)}</span>
    </div>
  {:else}
    <div class="text-xs text-fg-faint">No dedicated memory, plans place bytes in the host pool</div>
  {/if}
</div>
