<script lang="ts">
  import { bytes, deltaBytes, enumLabel } from '$lib/format';
  import { PoolKind } from '$proto/host_pb';
  import { TensorGroupKind } from '$proto/model_pb';
  import type { MemoryPlan, Placement } from '$proto/estimate_pb';
  import { poolName } from '$lib/state.svelte';
  import StackBar from './ui/StackBar.svelte';
  import ParamList from './ui/ParamList.svelte';

  // A memory plan in numbers: what weights, cache, and overhead add up to, how each pool fills, which tensor
  // kinds landed on which side, what stays on disk, and the params a run renders as flags
  let { plan, compact = false, bars = true, params = true }: { plan: MemoryPlan; compact?: boolean; bars?: boolean; params?: boolean } = $props();

  const kinds: Record<number, string> = { [PoolKind.DEVICE]: 'device', [PoolKind.HOST]: 'host', [PoolKind.UNIFIED]: 'unified' };
  const short: Record<number, string> = { [PoolKind.DEVICE]: 'GPU', [PoolKind.HOST]: 'RAM', [PoolKind.UNIFIED]: 'MEM' };
  // Template defaults render at launch, so only concrete values are worth showing
  const solved = $derived(Object.fromEntries(Object.entries(plan.params).filter(([, v]) => !v.includes('{{'))));
  // Placements name the side they sit on, device or host, not the pool, and a unified pool listed twice
  // carries the device share first and the host share second
  const sides = $derived.by(() => {
    const out: string[] = [];
    const seen = new Set<string>();
    for (const p of plan.pools) {
      out.push(p.kind === PoolKind.HOST || (p.kind === PoolKind.UNIFIED && seen.has(p.poolId)) ? 'host' : 'device');
      seen.add(p.poolId);
    }
    return out;
  });
  const onDisk = $derived(plan.skipped.reduce((a, p) => a + p.bytes, 0n));
  function placed(side: string): Placement[] {
    return plan.placements.filter((p) => p.poolId === side);
  }
  function kindWord(p: Placement): string {
    const word = enumLabel(TensorGroupKind, p.kind);
    return p.count > 1 ? `${word} ×${p.count}` : word;
  }
</script>

<div class="flex flex-col gap-3">
  <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs tabular-nums text-fg-faint">
    <span>weights <span class="text-fg-muted">{bytes(plan.weightsBytes)}</span></span>
    <span>cache <span class="text-fg-muted">{bytes(plan.cacheBytes)}</span></span>
    <span>overhead <span class="text-fg-muted">{bytes(plan.overheadBytes)}</span>{#if plan.overheadDelta}<span title="Learned from earlier runs of this runtime"> ({deltaBytes(plan.overheadDelta)} learned)</span>{/if}</span>
    {#if onDisk > 0n}<span title="Weights the runtime leaves on disk under these params">on disk <span class="text-fg-muted">{bytes(onDisk)}</span></span>{/if}
  </div>
  {#each plan.pools as pool, i (pool.poolId + i)}
    {@const parts = placed(sides[i])}
    {@const weights = parts.reduce((a, p) => a + p.bytes, 0n)}
    {@const rest = pool.usedBytes > weights ? pool.usedBytes - weights : 0n}
    {@const over = pool.usedBytes > pool.capacityBytes}
    {#if bars || (!compact && parts.length)}
      <div class="flex flex-col gap-1.5">
        {#if bars}
          <div class="min-w-0 truncate text-xs text-fg" title={pool.poolId}>{poolName(pool.poolId)} <span class="text-fg-faint">{kinds[pool.kind] ?? ''}</span></div>
          <StackBar
            max={pool.capacityBytes}
            segments={weights
              ? [
                  { label: 'weights', value: weights, tone: over ? 'bad' : 'accent' },
                  { label: 'cache + overhead', value: rest, tone: over ? 'bad' : 'info' }
                ]
              : [{ label: 'used', value: pool.usedBytes, tone: over ? 'bad' : 'accent' }]}
          />
        {/if}
        {#if !compact && parts.length}
          <div class="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-xs tabular-nums text-fg-faint">
            {#if !bars}<span class="w-7 shrink-0 text-fg-muted">{short[pool.kind] ?? 'pool'}</span>{/if}
            {#each parts as part (part.kind)}
              <span>{kindWord(part)} <span class="text-fg-muted">{bytes(part.bytes)}</span></span>
            {/each}
          </div>
        {/if}
      </div>
    {/if}
  {/each}
  {#if !compact && plan.skipped.length}
    <div class="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-xs tabular-nums text-fg-faint">
      {#if !bars}<span class="w-7 shrink-0 text-fg-muted">Disk</span>{/if}
      {#each plan.skipped as part (part.kind)}
        <span>{kindWord(part)} <span class="text-fg-muted">{bytes(part.bytes)}</span></span>
      {/each}
      <span>not loaded under these params</span>
    </div>
  {/if}
  {#if params && !compact && Object.keys(solved).length}
    <ParamList params={solved} />
  {/if}
</div>
