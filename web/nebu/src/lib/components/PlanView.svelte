<script lang="ts">
  import { bytes, deltaBytes, enumLabel, pct, verdictWord, verdictTone } from '$lib/format';
  import { PoolKind } from '$proto/host_pb';
  import { TensorGroupKind } from '$proto/model_pb';
  import type { MemoryPlan } from '$proto/estimate_pb';
  import Meter from './ui/Meter.svelte';
  import Pill from './ui/Pill.svelte';
  import ParamList from './ui/ParamList.svelte';

  // A memory plan: the verdict, what the bytes are, and how each pool fills
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
</script>

<div class="flex flex-col gap-4">
  <div class="flex flex-wrap items-center gap-x-5 gap-y-2 text-sm">
    <Pill tone={verdictTone(plan.verdict)} dot label={verdictWord(plan.verdict)} />
    <span class="text-fg-muted">weights <span class="tabular-nums text-fg">{bytes(plan.weightsBytes)}</span></span>
    <span class="text-fg-muted">cache <span class="tabular-nums text-fg">{bytes(plan.cacheBytes)}</span></span>
    <span class="text-fg-muted">
      overhead <span class="tabular-nums text-fg">{bytes(plan.overheadBytes)}</span>
      {#if plan.overheadDelta}<span class="text-xs text-fg-faint" title="Corrected by earlier runs of this runtime">({deltaBytes(plan.overheadDelta)} learned)</span>{/if}
    </span>
  </div>
  {#if plan.detail}<p class="text-sm leading-6 text-fg-muted">{plan.detail}</p>{/if}
  <div class="flex flex-col gap-3">
    {#each plan.pools as pool (pool.poolId)}
      {@const p = pct(pool.usedBytes, pool.capacityBytes)}
      <div class="flex flex-col gap-1.5">
        <div class="flex items-center justify-between gap-3 text-sm">
          <span class="min-w-0 truncate font-mono text-fg">{pool.poolId} <span class="font-sans text-fg-faint">{kinds[pool.kind] ?? ''}</span></span>
          <span class="shrink-0 tabular-nums text-fg-muted">{bytes(pool.usedBytes)} of {bytes(pool.capacityBytes)} · {p.toFixed(0)}%</span>
        </div>
        <Meter value={pool.usedBytes} max={pool.capacityBytes} tone={p > 100 ? 'bad' : p > 92 ? 'warn' : 'accent'} />
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
