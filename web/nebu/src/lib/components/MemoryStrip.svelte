<script lang="ts">
  import { bytes, pct } from '$lib/format';
  import { DeviceKind, PoolKind, type HostProfile } from '$proto/host_pb';
  import Meter from './ui/Meter.svelte';

  // Every accelerator and the host's own memory as one tile each, what is used right now over what there is
  let { host }: { host: HostProfile } = $props();

  interface Tile {
    id: string;
    name: string;
    total: bigint;
    free: bigint;
  }

  const tiles = $derived.by((): Tile[] => {
    const out: Tile[] = [];
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

<div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
  {#each tiles as t (t.id)}
    {@const used = t.total > t.free ? t.total - t.free : 0n}
    <a href="/host" class="card flex flex-col gap-2.5 px-4 py-3.5 transition-colors hover:border-line-strong">
      <div class="flex items-baseline gap-2">
        <span class="min-w-0 flex-1 truncate text-sm font-medium text-fg" title={t.name}>{t.name}</span>
        <span class="text-xs tabular-nums text-fg-faint">{pct(used, t.total).toFixed(0)}%</span>
      </div>
      <Meter value={used} max={t.total} />
      <div class="text-xs tabular-nums text-fg-muted"><span class="text-fg">{bytes(used)}</span> of {bytes(t.total)}</div>
    </a>
  {/each}
</div>
