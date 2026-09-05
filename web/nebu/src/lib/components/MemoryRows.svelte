<script lang="ts">
  import type { Component } from 'svelte';
  import { bytes, pct, enumLabel } from '$lib/format';
  import { DeviceKind, PoolKind, type HostProfile } from '$proto/host_pb';
  import { Cpu, MemoryStick, Microchip, ChevronRight } from '@lucide/svelte';
  import Meter from './ui/Meter.svelte';
  import Kv from './ui/Kv.svelte';

  interface Row {
    id: string;
    icon: Component<any>;
    name: string;
    sub: string;
    mono: string;
    total: bigint;
    free: bigint;
    facts: [string, string][];
  }

  // One row per device, then host memory, a device row unfolding what the probes reported about it
  let { host }: { host: HostProfile } = $props();

  const hostPool = $derived(host.pools.find((p) => p.kind === PoolKind.HOST));

  function poolOf(deviceId: string) {
    return host.pools.find((p) => p.deviceId === deviceId && p.kind !== PoolKind.HOST);
  }
  const rows = $derived.by((): Row[] => {
    const out: Row[] = host.devices.map((d) => {
      const pool = poolOf(d.id);
      return {
        id: d.id,
        icon: d.kind === DeviceKind.CPU ? Cpu : Microchip,
        name: d.name || d.id,
        sub: `${d.vendor} ${enumLabel(DeviceKind, d.kind)}`.trim(),
        mono: d.id,
        total: pool?.totalBytes ?? d.memoryTotalBytes,
        free: pool?.freeBytes ?? d.memoryFreeBytes,
        facts: Object.entries(d.facts).sort(([a], [b]) => a.localeCompare(b))
      };
    });
    if (hostPool) out.push({ id: hostPool.id, icon: MemoryStick, name: 'Host memory', sub: 'system RAM', mono: '', total: hostPool.totalBytes, free: hostPool.freeBytes, facts: [] });
    return out;
  });
</script>

{#snippet row(r: Row, fold: boolean)}
  {@const Icon = r.icon}
  {@const used = r.total > r.free ? r.total - r.free : 0n}
  <div class="grid grid-cols-[1rem_minmax(0,18rem)_minmax(0,1fr)_auto_1rem] items-center gap-x-5 px-2 py-3">
    <Icon size={16} class="text-fg-faint" />
    <div class="min-w-0">
      <div class="truncate text-sm font-medium text-fg" title={r.name}>{r.name}</div>
      <div class="truncate text-xs text-fg-faint">{r.sub}{#if r.mono}{' · '}<span class="font-mono">{r.mono}</span>{/if}</div>
    </div>
    {#if r.total > 0n}
      <Meter value={used} max={r.total} />
      <div class="text-right text-sm tabular-nums whitespace-nowrap">
        <span class="text-fg">{bytes(used)}</span>
        <span class="text-fg-faint">{' / '}{bytes(r.total)}</span>
        <span class="ml-3 inline-block w-9 text-right text-fg-muted">{pct(used, r.total).toFixed(0)}%</span>
      </div>
    {:else}
      <div class="col-span-2 text-sm text-fg-faint">No dedicated memory</div>
    {/if}
    <span class="flex w-4 justify-center text-fg-faint">{#if fold}<ChevronRight size={14} class="transition-transform group-open:rotate-90" />{/if}</span>
  </div>
{/snippet}

<div class="divide-y divide-line/70 border-y border-line">
  {#each rows as r (r.id)}
    {#if r.facts.length}
      <details class="group">
        <summary class="cursor-pointer list-none transition-colors select-none hover:bg-raised/40 [&::-webkit-details-marker]:hidden">{@render row(r, true)}</summary>
        <div class="bg-sunken/40 px-2 py-4 pl-[3.75rem]">
          <Kv mono columns={2} items={r.facts} />
        </div>
      </details>
    {:else}
      {@render row(r, false)}
    {/if}
  {:else}
    <div class="px-2 py-6 text-center text-sm text-fg-faint">No devices</div>
  {/each}
</div>
