<script lang="ts">
  import { bytes, deltaBytes, enumLabel, verdictWord, verdictTone } from '$lib/format';
  import { PoolKind } from '$proto/host_pb';
  import { TensorGroupKind } from '$proto/model_pb';
  import type { MemoryPlan } from '$proto/estimate_pb';
  import State from './ui/State.svelte';
  import StackBar from './ui/StackBar.svelte';
  import ParamList from './ui/ParamList.svelte';

  // A memory plan: the verdict, how each pool fills with weights and the rest, and what was placed where
  let { plan, compact = false }: { plan: MemoryPlan; compact?: boolean } = $props();

  const kinds: Record<number, string> = { [PoolKind.DEVICE]: 'device', [PoolKind.HOST]: 'host', [PoolKind.UNIFIED]: 'unified' };
  // Template defaults render at launch, so only concrete values are worth showing
  const solved = $derived(Object.fromEntries(Object.entries(plan.params).filter(([, v]) => !v.includes('{{'))));
  const placementsByPool = $derived.by(() => {
    const out = new Map<string, { kind: string; bytes: bigint; count: number }[]>();
    for (const p of plan.placements) {
      const list = out.get(p.poolId) ?? [];
      list.push({ kind: enumLabel(TensorGroupKind, p.kind), bytes: p.bytes, count: p.count });
      out.set(p.poolId, list);
    }
    return out;
  });
  function weightsIn(poolId: string): bigint {
    return (placementsByPool.get(poolId) ?? []).reduce((a, p) => a + p.bytes, 0n);
  }
</script>

<div class="flex flex-col gap-4">
  <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
    <State tone={verdictTone(plan.verdict)} label={verdictWord(plan.verdict)} />
    <span class="text-fg-faint">weights <span class="tabular-nums text-fg-muted">{bytes(plan.weightsBytes)}</span></span>
    <span class="text-fg-faint">cache <span class="tabular-nums text-fg-muted">{bytes(plan.cacheBytes)}</span></span>
    <span class="text-fg-faint">
      overhead <span class="tabular-nums text-fg-muted">{bytes(plan.overheadBytes)}</span>
      {#if plan.overheadDelta}<span class="text-xs" title="Learned from earlier runs of this runtime">({deltaBytes(plan.overheadDelta)} learned)</span>{/if}
    </span>
  </div>
  {#if plan.detail}<p class="text-sm leading-6 text-fg-muted">{plan.detail}</p>{/if}
  <div class="flex flex-col gap-3">
    {#each plan.pools as pool (pool.poolId)}
      {@const weights = weightsIn(pool.poolId)}
      {@const rest = pool.usedBytes > weights ? pool.usedBytes - weights : 0n}
      <div class="flex flex-col gap-1.5">
        <div class="flex items-center justify-between gap-3 text-sm">
          <span class="min-w-0 truncate font-mono text-xs text-fg">{pool.poolId} <span class="font-sans text-fg-faint">{kinds[pool.kind] ?? ''}</span></span>
        </div>
        <StackBar
          max={pool.capacityBytes}
          segments={weights
            ? [
                { label: 'weights', value: weights, tone: 'accent' },
                { label: 'cache + overhead', value: rest, tone: 'info' }
              ]
            : [{ label: 'used', value: pool.usedBytes, tone: 'accent' }]}
        />
        {#if !compact}
          {@const parts = placementsByPool.get(pool.poolId) ?? []}
          {#if parts.length}
            <div class="flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-fg-faint">
              {#each parts as part (part.kind)}
                <span>{part.kind} <span class="tabular-nums text-fg-muted">{bytes(part.bytes)}</span>{#if part.count > 1}<span> × {part.count}</span>{/if}</span>
              {/each}
            </div>
          {/if}
        {/if}
      </div>
    {/each}
  </div>
  {#if !compact && Object.keys(solved).length}
    <ParamList params={solved} />
  {/if}
</div>
