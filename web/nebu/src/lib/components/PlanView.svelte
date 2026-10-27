<script lang="ts" module>
  import type { MemoryPlan } from '$proto/estimate_pb';

  export function solvedParams(plan: MemoryPlan): Record<string, string> {
    return Object.fromEntries(Object.entries(plan.params).filter(([, v]) => v !== 'auto'));
  }
</script>

<script lang="ts">
  import { ago, bytes, ctx, deltaBytes, enumLabel, plural } from '$lib/format';
  import { verdictLabel, verdictTone } from '$lib/mesh';
  import { PoolKind } from '$proto/host_pb';
  import { TensorGroupKind } from '$proto/model_pb';
  import { FitVerdict, type GroupPlacement as Placement } from '$proto/estimate_pb';
  import { ParamType, type Param } from '$proto/runtime_pb';
  import type { Measurement } from '$proto/instance_pb';
  import { clock, instancesOnPool, poolName } from '$lib/state.svelte';
  import { pathLabel } from '$lib/diffusion';
  import SizeBar from './ui/SizeBar.svelte';
  import State from './ui/State.svelte';

  let {
    plan,
    compact = false,
    instance = '',
    params = [],
    measurements = []
  }: {
    plan: MemoryPlan;
    compact?: boolean;
    // The instance the plan belongs to, left out of the other instances on each pool
    instance?: string;
    // The runtime's parameters, which name the files and values the plan resolved
    params?: Param[];
    // What the runtime reported once it ran, compared with the plan
    measurements?: Measurement[];
  } = $props();

  const kinds: Record<number, string> = { [PoolKind.DEVICE]: 'device', [PoolKind.HOST]: 'host', [PoolKind.UNIFIED]: 'unified' };
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
  const onDevice = $derived(plan.pools.reduce((a, p, i) => (sides[i] === 'device' ? a + p.usedBytes : a), 0n));
  const onHost = $derived(plan.pools.reduce((a, p, i) => (sides[i] === 'host' ? a + p.usedBytes : a), 0n));
  const onDisk = $derived(plan.skipped.reduce((a, p) => a + p.bytes, 0n));
  const over = $derived(plan.pools.some((p) => p.usedBytes > p.capacityBytes));
  const measured = $derived(measurements.find((m) => m.key === 'device.used'));
  const drift = $derived(measured && onDevice > 0n ? (Number(measured.bytes - onDevice) / Number(onDevice)) * 100 : 0);

  const kindWord = (k: TensorGroupKind) => enumLabel(TensorGroupKind, k);
  const nameOf = (poolId: string, i: number) => (poolName(poolId) === 'Unified memory' && sides[i] === 'host' ? 'Unified memory, host side' : poolName(poolId));

  // How each kind of weight landed, kinds with layers first
  interface Landing {
    kind: TensorGroupKind;
    word: string;
    device: Placement | undefined;
    host: Placement | undefined;
    disk: Placement | undefined;
    count: number;
  }
  const landings = $derived.by((): Landing[] => {
    const by = new Map<TensorGroupKind, Landing>();
    const at = (k: TensorGroupKind) => {
      let l = by.get(k);
      if (!l) by.set(k, (l = { kind: k, word: kindWord(k), device: undefined, host: undefined, disk: undefined, count: 0 }));
      return l;
    };
    for (const p of plan.placements) {
      const l = at(p.kind);
      if (p.poolId === 'host') l.host = p;
      else l.device = p;
      l.count += p.count;
    }
    for (const p of plan.skipped) at(p.kind).disk = p;
    return [...by.values()].sort((a, b) => b.count - a.count || a.kind - b.kind);
  });
  const layers = $derived(landings.find((l) => l.kind === TensorGroupKind.LAYER));
  const experts = $derived(landings.find((l) => l.kind === TensorGroupKind.EXPERTS));
  const context = $derived.by(() => {
    const v = plan.params['n_ctx'];
    const n = Number(v);
    return v && v !== 'auto' && Number.isFinite(n) && n > 0 ? n : 0;
  });
  const paramOf = (name: string) => params.find((p) => p.name === name);
  const placementGroups = new Set(['Context', 'Placement', 'Memory', 'Cache', 'Parallelism']);
  // Placement values the planner chose beyond the ones the landings and context show
  const chosen = $derived(
    Object.entries(solvedParams(plan)).filter(([k, v]) => {
      const p = paramOf(k);
      return p?.solved && p.type !== ParamType.PATH && placementGroups.has(p.group) && k !== 'n_ctx' && k !== 'n_gpu_layers' && k !== 'n_cpu_moe' && v !== 'none' && v !== '';
    })
  );
  const files = $derived(
    Object.entries(solvedParams(plan)).filter(([k, v]) => {
      const p = paramOf(k);
      return p?.type === ParamType.PATH && v !== 'none' && v !== '';
    })
  );

  // Separate nebu's allocations from other memory usage
  function overlays(pool: MemoryPlan['pools'][number]) {
    const whole = pool.totalBytes || pool.capacityBytes;
    const inUse = whole > pool.freeBytes ? whole - pool.freeBytes : 0n;
    const ours = instancesOnPool(pool.poolId, instance);
    const others = inUse > ours ? inUse - ours : 0n;
    const items = [];
    if (others > 0n) items.push({ size: others, tone: 'neutral' as const });
    if (ours > 0n) items.push({ label: instance ? 'other instances' : 'instances', size: ours, tone: 'info' as const });
    items.push({ label: instance ? 'this instance' : 'this run', size: pool.usedBytes, tone: pool.usedBytes > pool.capacityBytes ? ('bad' as const) : ('accent' as const) });
    return [{ start: 'left' as const, items }];
  }
  function placed(l: Landing): string {
    const parts: string[] = [];
    if (l.device) parts.push(l.count > 1 ? `${l.device.count} on GPU` : 'on GPU');
    if (l.host) parts.push(l.count > 1 ? `${l.host.count} in RAM` : 'in RAM');
    if (l.disk) parts.push(l.count > 1 ? `${l.disk.count} on disk` : 'on disk');
    return parts.join(' · ');
  }
</script>

<div class="flex min-w-0 flex-col gap-3">
  <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
    <State tone={verdictTone(plan.verdict)} label={verdictLabel(plan.verdict)} />
    <span class="text-sm tabular-nums text-fg">{bytes(onDevice)} on GPU</span>
    {#if onHost > 0n}<span class="text-sm tabular-nums text-fg-muted">{bytes(onHost)} in RAM</span>{/if}
    {#if plan.plannedAt}<span class="ml-auto text-xs text-fg-faint">{ago(plan.plannedAt, clock.now)}</span>{/if}
  </div>

  {#if measured}
    <div class="flex flex-wrap items-baseline gap-x-2 text-xs tabular-nums">
      <span class="text-fg">Measured {bytes(measured.bytes)} on GPU</span>
      {#if Math.abs(drift) >= 1}<span class={Math.abs(drift) >= 15 ? 'text-warn' : 'text-fg-muted'}>{drift > 0 ? '+' : '−'}{Math.abs(drift).toFixed(0)}% vs plan</span>{/if}
    </div>
  {/if}

  <div class="flex flex-col gap-3">
    {#each plan.pools as pool, i (pool.poolId + i)}
      {@const cap = pool.capacityBytes}
      {@const bad = pool.usedBytes > cap}
      <div class="flex min-w-0 flex-col gap-1">
        <div class="flex items-baseline justify-between gap-3 text-xs">
          <span class="min-w-0 truncate text-fg" title={`${pool.poolId} · ${kinds[pool.kind] ?? ''}`}>{nameOf(pool.poolId, i)}</span>
          <span class="shrink-0 tabular-nums {bad ? 'text-bad' : 'text-fg-muted'}">{bytes(pool.usedBytes)} / {bytes(cap, 0)}{#if plan.againstFree && cap !== (pool.totalBytes || cap)}<span class="text-fg-faint">{' '}free</span>{/if}</span>
        </div>
        <SizeBar dense={compact} total={pool.totalBytes || pool.capacityBytes} overlays={overlays(pool)} />
      </div>
    {/each}
  </div>

  <dl class="flex flex-wrap gap-x-4 gap-y-1 text-xs tabular-nums">
    <div class="flex gap-1.5"><dt class="text-fg-muted">Weights</dt><dd class="text-fg">{bytes(plan.weightsBytes)}</dd></div>
    {#if plan.cacheBytes > 0n}<div class="flex gap-1.5"><dt class="text-fg-muted">Cache</dt><dd class="text-fg">{bytes(plan.cacheBytes)}</dd></div>{/if}
    <div class="flex gap-1.5" title={plan.overheadDelta ? `${deltaBytes(plan.overheadDelta)} learned from measured runs` : undefined}><dt class="text-fg-muted">Overhead</dt><dd class="text-fg">{bytes(plan.overheadBytes)}</dd></div>
    {#if onDisk > 0n}<div class="flex gap-1.5" title="Tensors this runtime never loads with these parameters"><dt class="text-fg-muted">On disk</dt><dd class="text-fg">{bytes(onDisk)}</dd></div>{/if}
    <div class="flex gap-1.5"><dt class="text-fg-muted">Total</dt><dd class="text-fg">{bytes(total)}</dd></div>
  </dl>

  {#if context || layers || experts || chosen.length}
    <dl class="flex flex-wrap gap-x-4 gap-y-1 border-t border-line pt-2 text-xs tabular-nums">
      {#if context}<div class="flex gap-1.5"><dt class="text-fg-muted">Context</dt><dd class="text-fg" title={plural(context, 'token')}>{ctx(context)}</dd></div>{/if}
      {#if layers && layers.count > 0}<div class="flex gap-1.5"><dt class="text-fg-muted">Layers on GPU</dt><dd class="text-fg">{layers.device?.count ?? 0}/{layers.count}</dd></div>{/if}
      {#if experts && experts.count > 0}<div class="flex gap-1.5"><dt class="text-fg-muted">Experts on GPU</dt><dd class="text-fg">{experts.device?.count ?? 0}/{experts.count}</dd></div>{/if}
      {#each chosen as [k, v] (k)}<div class="flex gap-1.5"><dt class="text-fg-muted">{paramOf(k)?.label ?? k}</dt><dd class="font-mono text-fg">{v}</dd></div>{/each}
    </dl>
  {/if}

  {#if plan.detail && plan.verdict !== FitVerdict.FITS}
    <p class="text-xs leading-5 {over || plan.verdict === FitVerdict.NO ? 'text-bad' : 'text-warn'} wrap-anywhere">{plan.detail}</p>
  {/if}

  {#if !compact && landings.length}
    <div class="tbl-wrap contain-inline-size border-t border-line pt-2">
      <table class="tbl dense text-xs">
        <thead><tr><th>Weights</th><th>Where</th><th class="num">GPU</th><th class="num">RAM</th><th class="num">Disk</th></tr></thead>
        <tbody>
          {#each landings as l (l.kind)}
            <tr>
              <td class="text-fg">{l.word}</td>
              <td class="text-fg-muted">{placed(l)}</td>
              <td class="num">{#if l.device}{bytes(l.device.bytes)}{:else}<span class="text-fg-faint">–</span>{/if}</td>
              <td class="num">{#if l.host}{bytes(l.host.bytes)}{:else}<span class="text-fg-faint">–</span>{/if}</td>
              <td class="num">{#if l.disk}<span class="text-fg-faint">{bytes(l.disk.bytes)}</span>{:else}<span class="text-fg-faint">–</span>{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}

  {#if !compact && files.length}
    <dl class="flex flex-col gap-1 border-t border-line pt-2 text-xs">
      {#each files as [k, v] (k)}
        <div class="flex min-w-0 gap-2"><dt class="shrink-0 text-fg-muted">{paramOf(k)?.label ?? k}</dt><dd class="min-w-0 truncate font-mono text-fg" title={v}>{pathLabel(v)}</dd></div>
      {/each}
    </dl>
  {/if}
</div>
