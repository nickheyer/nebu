<script lang="ts">
  import { live, liveInstances, poolName, slotName } from '$lib/state.svelte';
  import { bytes, type Tone } from '$lib/format';
  import { PoolKind } from '$proto/host_pb';
  import type { SizeItem } from './ui/SizeBar.svelte';
  import SizeBar from './ui/SizeBar.svelte';

  // Each pool runs land on, with every live instance's planned share, other use, and free space
  const tones: Tone[] = ['accent', 'info', 'ok', 'warn'];
  const pools = $derived.by(() => {
    const host = live.host;
    if (!host) return [];
    const accelerated = host.pools.some((p) => p.kind !== PoolKind.HOST);
    const out: { id: string; name: string; total: bigint; used: bigint; items: SizeItem[] }[] = [];
    for (const p of host.pools) {
      if (p.kind === PoolKind.HOST && accelerated) continue;
      const items: SizeItem[] = [];
      let ours = 0n;
      liveInstances().forEach((i, n) => {
        const used = i.plan?.pools.filter((x) => x.poolId === p.id).reduce((a, x) => a + x.usedBytes, 0n) ?? 0n;
        if (used <= 0n) return;
        items.push({ label: i.slotId ? slotName(i.slotId) : i.name, size: used, tone: tones[n % tones.length] });
        ours += used;
      });
      const inUse = p.totalBytes > p.freeBytes ? p.totalBytes - p.freeBytes : 0n;
      const others = inUse > ours ? inUse - ours : 0n;
      if (others > 0n) items.unshift({ label: 'other', size: others, tone: 'neutral' });
      out.push({ id: p.id, name: poolName(p.id), total: p.totalBytes, used: inUse > ours ? inUse : ours, items });
    }
    return out;
  });
</script>

{#if pools.length}
  <div class="grid gap-x-8 gap-y-3 sm:grid-cols-2 xl:grid-cols-3">
    {#each pools as p (p.id)}
      <div class="flex min-w-0 flex-col gap-1">
        <div class="flex items-baseline justify-between gap-3 text-xs">
          <span class="min-w-0 truncate text-fg">{p.name}</span>
          <span class="shrink-0 tabular-nums text-fg-muted">{bytes(p.used)} / {bytes(p.total, 0)}</span>
        </div>
        <SizeBar total={p.total} overlays={[{ start: 'left', items: p.items }]} free="free" />
      </div>
    {/each}
  </div>
{/if}
