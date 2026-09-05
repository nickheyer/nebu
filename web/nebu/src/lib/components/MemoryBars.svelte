<script lang="ts">
  import { bytes, pct } from '$lib/format';
  import { DeviceKind, PoolKind, type HostProfile } from '$proto/host_pb';
  import Meter from './ui/Meter.svelte';

  // Every accelerator and the host's own memory as one bar each, what is used now over what there is
  let { host }: { host: HostProfile } = $props();

  interface Row {
    id: string;
    name: string;
    total: bigint;
    free: bigint;
  }

  const rows = $derived.by((): Row[] => {
    const out: Row[] = [];
    for (const d of host.devices) {
      if (d.kind === DeviceKind.CPU) continue;
      const pool = host.pools.find((p) => p.deviceId === d.id && p.kind !== PoolKind.HOST);
      const total = pool?.totalBytes ?? d.memoryTotalBytes;
      if (!total) continue;
      out.push({ id: d.id, name: d.name || d.id, total, free: pool?.freeBytes ?? d.memoryFreeBytes });
    }
    const hostPool = host.pools.find((p) => p.kind === PoolKind.HOST);
    if (hostPool) out.push({ id: hostPool.id, name: 'Host memory', total: hostPool.totalBytes, free: hostPool.freeBytes });
    return out;
  });
</script>

<div class="flex flex-col">
  {#each rows as r (r.id)}
    {@const used = r.total > r.free ? r.total - r.free : 0n}
    <a href="/host" class="grid h-9 grid-cols-[minmax(8rem,14rem)_minmax(0,1fr)_auto_2.5rem] items-center gap-4 rounded-md px-1.5 transition-colors hover:bg-raised/40">
      <span class="truncate text-sm text-fg" title={r.name}>{r.name}</span>
      <Meter value={used} max={r.total} />
      <span class="text-sm tabular-nums whitespace-nowrap"><span class="text-fg">{bytes(used)}</span><span class="text-fg-faint">{' / '}{bytes(r.total)}</span></span>
      <span class="text-right text-xs tabular-nums text-fg-faint">{pct(used, r.total).toFixed(0)}%</span>
    </a>
  {:else}
    <div class="px-1.5 py-2 text-sm text-fg-faint">No devices</div>
  {/each}
</div>
