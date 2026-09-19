<script lang="ts" module>
  import type { MemoryPlan } from '$proto/estimate_pb';

  export function solvedParams(plan: MemoryPlan): Record<string, string> {
    return Object.fromEntries(Object.entries(plan.params).filter(([, v]) => v !== 'auto'));
  }
</script>

<script lang="ts">
  import { ago, bytes, deltaBytes, enumLabel } from '$lib/format';
  import { PoolKind } from '$proto/host_pb';
  import { TensorGroupKind } from '$proto/model_pb';
  import type { GroupPlacement as Placement } from '$proto/estimate_pb';
  import { clock, instancesOnPool, poolName } from '$lib/state.svelte';
  import SizeBar from './ui/SizeBar.svelte';
  import ParamList from './ui/ParamList.svelte';

  let {
    plan,
    compact = false,
    params = true,
    except = ''
  }: { plan: MemoryPlan; compact?: boolean; params?: boolean; except?: string } = $props();

  const kinds: Record<number, string> = { [PoolKind.DEVICE]: 'device', [PoolKind.HOST]: 'host', [PoolKind.UNIFIED]: 'unified' };
  const solved = $derived(solvedParams(plan));
  const total = $derived(plan.weightsBytes + plan.cacheBytes + plan.overheadBytes);
  // A repeated unified pool lists device usage first, then host usage.
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
  // Separate nebu's allocations from other memory usage.
  function overlays(pool: MemoryPlan['pools'][number]) {
    const whole = pool.totalBytes || pool.capacityBytes;
    const inUse = whole > pool.freeBytes ? whole - pool.freeBytes : 0n;
    const ours = instancesOnPool(pool.poolId, except);
    const others = inUse > ours ? inUse - ours : 0n;
    const items = [];
    if (others > 0n) items.push({ size: others, tone: 'neutral' as const });
    if (ours > 0n) items.push({ label: 'instances', size: ours, tone: 'info' as const });
    items.push({ label: 'this run', size: pool.usedBytes, tone: pool.usedBytes > pool.capacityBytes ? ('bad' as const) : ('accent' as const) });
    return [{ start: 'left' as const, items }];
  }
</script>

<div class="flex min-w-0 flex-col gap-4">
  <div>
    <div class="flex flex-wrap items-baseline gap-x-2 gap-y-1">
      <span class="text-2xl font-semibold tracking-tight text-fg tabular-nums">{bytes(total)}</span>
      {#if plan.plannedAt}<span class="text-xs text-fg-faint">host read {ago(plan.plannedAt, clock.now)}</span>{/if}
    </div>
    <dl class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs tabular-nums">
      <div class="flex gap-1.5"><dt class="text-fg-muted">Weights</dt><dd>{bytes(plan.weightsBytes)}</dd></div>
      <div class="flex gap-1.5"><dt class="text-fg-muted">Cache</dt><dd>{bytes(plan.cacheBytes)}</dd></div>
      <div class="flex gap-1.5" title={plan.overheadDelta ? `${deltaBytes(plan.overheadDelta)} correction learned from measured runs` : undefined}><dt class="text-fg-muted">Overhead</dt><dd>{bytes(plan.overheadBytes)}</dd></div>
      {#if onDisk > 0n}<div class="flex gap-1.5" title="Tensors this runtime never loads with these parameters"><dt class="text-fg-muted">On disk</dt><dd>{bytes(onDisk)}</dd></div>{/if}
    </dl>
  </div>

  <div class="flex flex-col gap-4 border-t border-line pt-3">
    {#each plan.pools as pool, i (pool.poolId + i)}
      {@const parts = placed(sides[i])}
      <div class="flex min-w-0 flex-col gap-1.5">
        <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 text-xs">
          <span class="min-w-0 text-fg wrap-anywhere" title={`${pool.poolId} · ${kinds[pool.kind] ?? ''}`}>{poolName(pool.poolId)}</span>
          {#if !compact && parts.length}
            <span class="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 tabular-nums text-fg-faint">
              {#each parts as part (part.kind)}
                <span>{kindWord(part)} <span class="text-fg-muted">{bytes(part.bytes)}</span></span>
              {/each}
            </span>
          {/if}
        </div>
        <SizeBar total={pool.totalBytes || pool.capacityBytes} overlays={overlays(pool)} />
      </div>
    {/each}
  </div>
  {#if !compact && plan.skipped.length}
    <div class="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-xs tabular-nums text-fg-faint">
      {#each plan.skipped as part (part.kind)}
        <span>{kindWord(part)} <span class="text-fg-muted">{bytes(part.bytes)}</span></span>
      {/each}
      <span>stay on disk with these parameters</span>
    </div>
  {/if}
  {#if params && !compact && Object.keys(solved).length}
    <ParamList params={solved} />
  {/if}
</div>
