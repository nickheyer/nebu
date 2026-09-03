<script lang="ts">
  import { bytes, ctx, verdictLabel, verdictTone } from '$lib/format';
  import { PoolKind } from '$proto/host_pb';
  import type { FitRow, MemoryPlan } from '$proto/estimate_pb';
  import { FitVerdict } from '$proto/estimate_pb';
  import Tabs from './ui/Tabs.svelte';
  import Tip from './ui/Tip.svelte';
  import PlanView from './PlanView.svelte';

  let { rows, onPick }: { rows: FitRow[]; onPick?: (row: FitRow) => void } = $props();

  const runtimes = $derived([...new Set(rows.map((r) => r.runtimeId))]);
  let runtime = $state('');
  $effect(() => {
    if (!runtime || !runtimes.includes(runtime)) runtime = runtimes[0] ?? '';
  });
  const contexts = $derived([...new Set(rows.filter((r) => r.runtimeId === runtime).map((r) => r.context))].sort((a, b) => a - b));
  const groups = $derived([...new Set(rows.filter((r) => r.runtimeId === runtime).map((r) => r.group))]);

  function cell(group: string, c: number): FitRow | undefined {
    return rows.find((r) => r.runtimeId === runtime && r.group === group && r.context === c);
  }
  function device(plan?: MemoryPlan): string {
    if (!plan) return '';
    let used = 0n;
    for (const p of plan.pools) if (p.kind === PoolKind.DEVICE || p.kind === PoolKind.UNIFIED) used += p.usedBytes;
    return used ? bytes(used, 0) : '';
  }
  const cellTone: Record<string, string> = {
    ok: 'border-ok/30 bg-ok/10 text-ok hover:bg-ok/20',
    warn: 'border-warn/30 bg-warn/10 text-warn hover:bg-warn/20',
    bad: 'border-bad/25 bg-bad/8 text-bad/90 hover:bg-bad/15',
    neutral: 'border-line bg-raised text-fg-faint'
  };
  let selected = $state<FitRow | null>(null);
</script>

<div class="flex flex-col gap-3">
  {#if runtimes.length > 1}
    <Tabs tabs={runtimes.map((r) => ({ id: r, label: r }))} bind:value={runtime} size="sm" />
  {:else if runtimes.length === 1}
    <div class="text-xs text-fg-muted">planned on <span class="font-medium text-fg">{runtime}</span></div>
  {/if}
  <div class="overflow-x-auto">
    <table class="w-full border-separate border-spacing-1 text-xs">
      <thead>
        <tr>
          <th class="px-2 py-1 text-left text-[11px] font-semibold tracking-wider text-fg-faint uppercase">group</th>
          {#each contexts as c (c)}
            <th class="px-2 py-1 text-center text-[11px] font-semibold tracking-wider text-fg-faint uppercase">{ctx(c)}</th>
          {/each}
        </tr>
      </thead>
      <tbody>
        {#each groups as g (g)}
          <tr>
            <td class="px-2 py-1 font-mono whitespace-nowrap text-fg">{g}</td>
            {#each contexts as c (c)}
              {@const r = cell(g, c)}
              <td class="p-0">
                {#if r?.plan}
                  {@const t = verdictTone(r.plan.verdict)}
                  <Tip>
                    <button
                      class="flex h-10 w-full min-w-[4.5rem] flex-col items-center justify-center rounded-md border px-2 transition-colors {cellTone[t] ?? cellTone.neutral} {selected === r ? 'ring-2 ring-accent/60' : ''}"
                      onclick={() => {
                        selected = selected === r ? null : r;
                        onPick?.(r);
                      }}
                    >
                      <span class="font-semibold capitalize">{verdictLabel(r.plan.verdict)}</span>
                      {#if device(r.plan)}<span class="text-[10.5px] tabular-nums opacity-80">{device(r.plan)}</span>{/if}
                    </button>
                    {#snippet content()}
                      <div class="text-[11px] leading-5">
                        {#each r.plan?.pools ?? [] as p (p.poolId)}
                          <div><span class="font-mono text-fg-muted">{p.poolId}</span> {bytes(p.usedBytes)} of {bytes(p.capacityBytes)}</div>
                        {/each}
                        <div class="text-fg-muted">cache {bytes(r.plan?.cacheBytes)}, overhead {bytes(r.plan?.overheadBytes)}</div>
                        {#if r.plan?.detail}<div class="mt-1 text-fg-faint">{r.plan.detail}</div>{/if}
                      </div>
                    {/snippet}
                  </Tip>
                {:else}
                  <div class="flex h-10 w-full items-center justify-center rounded-md border border-line text-fg-faint">–</div>
                {/if}
              </td>
            {/each}
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  {#if selected?.plan}
    <div class="rounded-lg border border-line bg-sunken p-3">
      <div class="mb-2 text-xs text-fg-muted">
        <span class="font-mono text-fg">{selected.group}</span> on {selected.runtimeId} at {ctx(selected.context)} context
      </div>
      <PlanView plan={selected.plan} compact />
    </div>
  {/if}
  <div class="flex flex-wrap gap-3 text-[11px] text-fg-faint">
    <span class="flex items-center gap-1"><span class="h-2 w-2 rounded-sm bg-ok"></span>fits in device memory</span>
    <span class="flex items-center gap-1"><span class="h-2 w-2 rounded-sm bg-warn"></span>partial, spills to host</span>
    <span class="flex items-center gap-1"><span class="h-2 w-2 rounded-sm bg-bad"></span>does not fit</span>
  </div>
</div>
