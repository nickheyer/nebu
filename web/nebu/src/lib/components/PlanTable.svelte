<script lang="ts">
  import { bytes, deltaBytes, enumLabel, plural, ratioBytes } from '$lib/format';
  import { PoolKind } from '$proto/host_pb';
  import { TensorGroupKind } from '$proto/model_pb';
  import type { MemoryPlan, GroupPlacement as Placement } from '$proto/estimate_pb';
  import { poolName } from '$lib/state.svelte';
  import Tip from './ui/Tip.svelte';

  // A memory plan as a table: a column per side, device and host, a row per tensor kind with what it weighs on
  // each side, cache and overhead under the weights, and a footer of every pool against its capacity. A kind
  // the runtime never loads keeps its row, its bytes in the total column with the reason on hover.
  let { plan }: { plan: MemoryPlan } = $props();

  const short: Record<number, string> = { [PoolKind.DEVICE]: 'GPU', [PoolKind.HOST]: 'RAM', [PoolKind.UNIFIED]: 'MEM' };

  interface Pool {
    id: string;
    used: bigint;
    cap: bigint;
  }
  interface Side {
    id: string;
    label: string;
    pools: Pool[];
    shared: boolean;
    used: bigint;
    rest: bigint;
  }
  interface Row {
    kind: TensorGroupKind;
    word: string;
    on: Record<string, Placement>;
    disk?: Placement;
    total: bigint;
    count: number;
  }

  // Placements name the side they sit on, device or host, so pools of one side share a column; a unified pool
  // listed twice carries its device share first and its host share second, and each column says which. A side
  // the plan placed weights on without a pool to hold them has no usage to show beyond those weights
  const sides = $derived.by(() => {
    const out: Side[] = [];
    const seen = new Set<string>();
    const side = (id: string, label: string) => {
      let s = out.find((o) => o.id === id);
      if (!s) out.push((s = { id, label, pools: [], shared: false, used: 0n, rest: 0n }));
      return s;
    };
    for (const p of plan.pools) {
      const host = p.kind === PoolKind.HOST || (p.kind === PoolKind.UNIFIED && seen.has(p.poolId));
      seen.add(p.poolId);
      side(host ? 'host' : 'device', short[p.kind] ?? 'pool').pools.push({ id: p.poolId, used: p.usedBytes, cap: p.capacityBytes });
    }
    for (const p of plan.placements) {
      if (p.poolId) side(p.poolId, p.poolId === 'host' ? 'RAM' : 'GPU');
    }
    for (const s of out) {
      s.shared = s.pools.some((pool) => out.some((o) => o !== s && o.pools.some((q) => q.id === pool.id)));
      const weights = plan.placements.filter((p) => p.poolId === s.id).reduce((a, p) => a + p.bytes, 0n);
      s.used = s.pools.reduce((a, p) => a + p.used, 0n);
      s.rest = s.used > weights ? s.used - weights : 0n;
    }
    return out;
  });

  // One row per tensor kind, the loaded kinds in the planner's order and the kinds left on disk after them
  const rows = $derived.by(() => {
    const out: Row[] = [];
    const row = (kind: TensorGroupKind) => {
      let r = out.find((o) => o.kind === kind);
      if (!r) out.push((r = { kind, word: enumLabel(TensorGroupKind, kind), on: {}, total: 0n, count: 0 }));
      return r;
    };
    for (const p of plan.placements) {
      const r = row(p.kind);
      r.on[p.poolId] = p;
      r.total += p.bytes;
      r.count += p.count;
    }
    for (const p of plan.skipped) row(p.kind).disk = p;
    return out;
  });
  const onDisk = $derived(plan.skipped.reduce((a, p) => a + p.bytes, 0n));
  const need = $derived(plan.weightsBytes + plan.cacheBytes + plan.overheadBytes);
  const skippedWords = $derived(plan.skipped.map((p) => `${enumLabel(TensorGroupKind, p.kind)} ×${p.count} ${bytes(p.bytes)}`).join(', '));
</script>

{#snippet amount(p: { bytes: bigint; count: number } | undefined)}
  {#if p}{#if p.count > 1}<span class="mr-1 text-fg-faint">×{p.count}</span>{/if}{bytes(p.bytes)}{:else}<span class="text-fg-faint">–</span>{/if}
{/snippet}

<div class="contain-inline-size overflow-x-auto">
  <table class="tbl dense text-xs">
    <thead>
      <tr>
        <th></th>
        {#each sides as side (side.id)}
          <th class="num" title={side.pools.map((p) => p.id).join(', ')}>
            {side.label}
            {#if side.pools.length}
              <span class="ml-1 inline-block max-w-40 truncate align-bottom font-normal tracking-normal normal-case">
                {side.pools.length === 1 ? poolName(side.pools[0].id) : plural(side.pools.length, 'pool')}{side.shared ? ` · ${side.id}` : ''}
              </span>
            {/if}
          </th>
        {/each}
        <th class="num">Total</th>
      </tr>
    </thead>
    <tbody>
      {#each rows as row (row.kind)}
        <tr>
          <td class="text-fg-muted">{row.word}</td>
          {#each sides as side (side.id)}
            <td class="num">{@render amount(row.on[side.id])}</td>
          {/each}
          <td class="num">
            {#if row.disk}
              <Tip text="Stays on disk. This runtime never loads {row.word} tensors with these parameters.">
                <span class="text-fg-faint">{#if row.disk.count > 1}<span class="mr-1">×{row.disk.count}</span>{/if}{bytes(row.disk.bytes)} on disk</span>
              </Tip>
            {:else}
              {@render amount({ bytes: row.total, count: row.count })}
            {/if}
          </td>
        </tr>
      {/each}
      <tr>
        <td class="text-fg-muted">cache</td>
        {#each sides as side (side.id)}
          <td class="num !border-b-0" rowspan="2" title="Cache and overhead together on this side">
            {#if side.used > 0n}{bytes(side.rest)}{:else}<span class="text-fg-faint">–</span>{/if}
          </td>
        {/each}
        <td class="num">{bytes(plan.cacheBytes)}</td>
      </tr>
      <tr>
        <td class="text-fg-muted">overhead</td>
        <td class="num">
          {bytes(plan.overheadBytes)}
          {#if plan.overheadDelta}
            <Tip text="Correction learned from measured runs"><span class="ml-1.5 text-fg-faint">{deltaBytes(plan.overheadDelta)} learned</span></Tip>
          {/if}
        </td>
      </tr>
    </tbody>
    <tfoot>
      <tr>
        <td>total</td>
        {#each sides as side (side.id)}
          <td class="num">
            {#each side.pools as pool (pool.id)}
              <div class="flex justify-end gap-3">
                {#if side.pools.length > 1}<span class="truncate font-normal text-fg-faint">{poolName(pool.id)}</span>{/if}
                <span class={pool.used > pool.cap ? 'text-bad' : ''}>
                  {ratioBytes(pool.used, pool.cap)}{#if plan.againstFree}<span class="ml-1 font-normal text-fg-faint">free</span>{/if}
                </span>
              </div>
            {/each}
            {#if !side.pools.length}<span class="text-fg-faint">–</span>{/if}
          </td>
        {/each}
        <td class="num">
          {#if onDisk > 0n}
            <Tip text="Loaded into memory. Another {bytes(onDisk)} stays on disk: {skippedWords}.">{bytes(need)}</Tip>
          {:else}
            {bytes(need)}
          {/if}
        </td>
      </tr>
    </tfoot>
  </table>
</div>
