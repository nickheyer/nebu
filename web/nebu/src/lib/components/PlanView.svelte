<script lang="ts">
  import { bytes, enumLabel, pct, verdictLabel, verdictTone } from '$lib/format';
  import { PoolKind } from '$proto/host_pb';
  import { TensorGroupKind } from '$proto/model_pb';
  import type { MemoryPlan } from '$proto/estimate_pb';
  import Badge from './ui/Badge.svelte';
  import Meter from './ui/Meter.svelte';

  let { plan, compact = false }: { plan: MemoryPlan; compact?: boolean } = $props();

  const kinds: Record<number, string> = { [PoolKind.DEVICE]: 'device', [PoolKind.HOST]: 'host', [PoolKind.UNIFIED]: 'unified' };
  // Template defaults render at launch, so only concrete values are worth showing
  const solved = $derived(Object.entries(plan.params).filter(([, v]) => !v.includes('{{')));
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
  <div class="flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
    <Badge tone={verdictTone(plan.verdict)} dot label={verdictLabel(plan.verdict)} />
    <span class="text-fg-muted">weights <span class="text-fg tabular-nums">{bytes(plan.weightsBytes)}</span></span>
    <span class="text-fg-muted">cache <span class="text-fg tabular-nums">{bytes(plan.cacheBytes)}</span></span>
    <span class="text-fg-muted">overhead <span class="text-fg tabular-nums">{bytes(plan.overheadBytes)}</span></span>
  </div>
  {#if plan.detail}<p class="text-sm leading-6 text-fg-muted">{plan.detail}</p>{/if}
  <div class="flex flex-col gap-3">
    {#each plan.pools as pool (pool.poolId)}
      {@const p = pct(pool.usedBytes, pool.capacityBytes)}
      <div class="flex flex-col gap-1.5">
        <div class="flex items-center justify-between text-xs">
          <span class="font-mono text-fg">{pool.poolId} <span class="text-fg-faint">{kinds[pool.kind] ?? ''}</span></span>
          <span class="tabular-nums text-fg-muted">{bytes(pool.usedBytes)} of {bytes(pool.capacityBytes)} · {p.toFixed(0)}%</span>
        </div>
        <Meter value={pool.usedBytes} max={pool.capacityBytes} tone={p > 100 ? 'bad' : p > 92 ? 'warn' : 'accent'} />
        {#if !compact}
          {@const parts = placementsByPool.get(pool.poolId) ?? []}
          {#if parts.length}
            <div class="flex flex-wrap gap-x-3 gap-y-0.5 text-[11.5px] text-fg-faint">
              {#each parts as part (part.kind)}
                <span>{part.kind} <span class="tabular-nums text-fg-muted">{bytes(part.bytes)}</span>{#if part.count > 1}<span> × {part.count}</span>{/if}</span>
              {/each}
            </div>
          {/if}
        {/if}
      </div>
    {/each}
  </div>
  {#if !compact && solved.length}
    <div class="flex flex-wrap gap-1.5">
      {#each solved as [k, v] (k)}
        <span class="rounded border border-line bg-sunken px-1.5 py-0.5 font-mono text-[11px]"><span class="text-fg-faint">{k}=</span><span class="text-fg">{v}</span></span>
      {/each}
    </div>
  {/if}
</div>
