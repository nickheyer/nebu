<script lang="ts">
  import { bytes, pct, enumLabel } from '$lib/format';
  import { DeviceKind, PoolKind, type HostProfile } from '$proto/host_pb';
  import { Cpu, MemoryStick, Microchip } from '@lucide/svelte';
  import Meter from './ui/Meter.svelte';

  // One row per accelerator, then host memory, each a meter over what is free right now
  let { host, cpu = false }: { host: HostProfile; cpu?: boolean } = $props();

  const devices = $derived(host.devices.filter((d) => cpu || d.kind !== DeviceKind.CPU));
  const hostPool = $derived(host.pools.find((p) => p.kind === PoolKind.HOST));

  function poolOf(deviceId: string) {
    return host.pools.find((p) => p.deviceId === deviceId && p.kind !== PoolKind.HOST);
  }
  const rows = $derived.by(() => {
    const out = devices.map((d) => {
      const pool = poolOf(d.id);
      const total = pool?.totalBytes ?? d.memoryTotalBytes;
      const free = pool?.freeBytes ?? d.memoryFreeBytes;
      return { id: d.id, icon: d.kind === DeviceKind.CPU ? Cpu : Microchip, name: d.name || d.id, sub: `${d.vendor} ${enumLabel(DeviceKind, d.kind)}`.trim(), mono: d.id, total, free };
    });
    if (hostPool) out.push({ id: hostPool.id, icon: MemoryStick, name: 'Host memory', sub: 'system RAM', mono: '', total: hostPool.totalBytes, free: hostPool.freeBytes });
    return out;
  });
</script>

<div class="divide-y divide-line/70 rounded-lg border border-line">
  {#each rows as r (r.id)}
    {@const Icon = r.icon}
    {@const used = r.total > r.free ? r.total - r.free : 0n}
    <div class="grid grid-cols-[auto_minmax(0,14rem)_minmax(0,1fr)_auto] items-center gap-x-4 px-4 py-2.5">
      <Icon size={15} class="text-fg-faint" />
      <div class="min-w-0">
        <div class="truncate text-sm font-medium text-fg" title={r.name}>{r.name}</div>
        <div class="truncate text-[11px] text-fg-faint">{r.sub}{#if r.mono}{' · '}<span class="font-mono">{r.mono}</span>{/if}</div>
      </div>
      {#if r.total > 0n}
        <Meter value={used} max={r.total} height="md" />
        <div class="w-40 text-right text-xs tabular-nums">
          <span class="text-fg">{bytes(used)}</span>
          <span class="text-fg-faint"> / {bytes(r.total)}</span>
          <span class="ml-2 inline-block w-9 text-right text-fg-muted">{pct(used, r.total).toFixed(0)}%</span>
        </div>
      {:else}
        <div class="col-span-2 text-xs text-fg-faint">no dedicated memory</div>
      {/if}
    </div>
  {:else}
    <div class="px-4 py-6 text-center text-sm text-fg-faint">No devices found</div>
  {/each}
</div>
