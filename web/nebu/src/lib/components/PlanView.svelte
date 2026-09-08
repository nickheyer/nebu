<script lang="ts" module>
  import type { MemoryPlan } from '$proto/estimate_pb';

  // The params a plan solved to concrete values; template defaults render at launch and are left out
  export function solvedParams(plan: MemoryPlan): Record<string, string> {
    return Object.fromEntries(Object.entries(plan.params).filter(([, v]) => !v.includes('{{')));
  }
</script>

<script lang="ts">
  import { bytes, deltaBytes, enumLabel } from '$lib/format';
  import { PoolKind } from '$proto/host_pb';
  import { TensorGroupKind } from '$proto/model_pb';
  import type { Placement } from '$proto/estimate_pb';
  import { poolName } from '$lib/state.svelte';
  import StackBar from './ui/StackBar.svelte';
  import ParamList from './ui/ParamList.svelte';

  // The estimate total and its breakdown, followed by one usage row for each memory pool.
  let { plan, compact = false, params = true }: { plan: MemoryPlan; compact?: boolean; params?: boolean } = $props();

  const kinds: Record<number, string> = { [PoolKind.DEVICE]: 'device', [PoolKind.HOST]: 'host', [PoolKind.UNIFIED]: 'unified' };
  const solved = $derived(solvedParams(plan));
  const total = $derived(plan.weightsBytes + plan.cacheBytes + plan.overheadBytes);
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

<div class="flex min-w-0 flex-col gap-4">
  <div>
    <div class="flex flex-wrap items-baseline gap-x-2 gap-y-1">
      <span class="text-2xl font-semibold tracking-tight text-fg tabular-nums">{bytes(total)}</span>
      <span class="text-xs text-fg-muted">estimated</span>
    </div>
    <dl class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs tabular-nums">
      <div class="flex gap-1.5"><dt class="text-fg-muted">Weights</dt><dd>{bytes(plan.weightsBytes)}</dd></div>
      <div class="flex gap-1.5"><dt class="text-fg-muted">Cache</dt><dd>{bytes(plan.cacheBytes)}</dd></div>
      <div class="flex gap-1.5" title={plan.overheadDelta ? `${deltaBytes(plan.overheadDelta)} correction learned from measured runs` : undefined}><dt class="text-fg-muted">Overhead</dt><dd>{bytes(plan.overheadBytes)}</dd></div>
      {#if onDisk > 0n}<div class="flex gap-1.5" title="Weights the runtime leaves on disk with these parameters"><dt class="text-fg-muted">Not loaded</dt><dd>{bytes(onDisk)}</dd></div>{/if}
    </dl>
  </div>

  <div class="flex flex-col gap-3 border-t border-line pt-3">
    {#each plan.pools as pool, i (pool.poolId + i)}
      {@const parts = placed(sides[i])}
      {@const over = pool.usedBytes > pool.capacityBytes}
      <div class="flex min-w-0 flex-col gap-2">
        <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 text-xs">
          <span class="min-w-0 text-fg wrap-anywhere" title={`${pool.poolId} · ${kinds[pool.kind] ?? ''}`}>{poolName(pool.poolId)}</span>
          <span class="tabular-nums {over ? 'text-bad' : 'text-fg-muted'}"><span class={over ? 'text-bad' : 'text-fg'}>{bytes(pool.usedBytes)}</span> / {bytes(pool.capacityBytes)} {plan.againstFree ? 'free' : 'total'}</span>
        </div>
        {#if pool.usedBytes > 0n}
          <StackBar max={pool.capacityBytes} legend={false} height="sm" segments={[{ label: 'Estimated usage', value: pool.usedBytes, tone: over ? 'bad' : 'accent' }]} />
        {/if}
        {#if !compact && parts.length}
          <div class="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-xs tabular-nums text-fg-faint">
            {#each parts as part (part.kind)}
              <span>{kindWord(part)} <span class="text-fg-muted">{bytes(part.bytes)}</span></span>
            {/each}
          </div>
        {/if}
      </div>
    {/each}
  </div>
  {#if !compact && plan.skipped.length}
    <div class="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-xs tabular-nums text-fg-faint">
      {#each plan.skipped as part (part.kind)}
        <span>{kindWord(part)} <span class="text-fg-muted">{bytes(part.bytes)}</span></span>
      {/each}
      <span>not loaded with these parameters</span>
    </div>
  {/if}
  {#if params && !compact && Object.keys(solved).length}
    <ParamList params={solved} />
  {/if}
</div>
