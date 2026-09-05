<script lang="ts">
  import type { Component } from 'svelte';
  import { bytes, pct, enumLabel } from '$lib/format';
  import { DeviceKind, PoolKind, type HostProfile } from '$proto/host_pb';
  import { Cpu, MemoryStick, Microchip, ChevronDown } from '@lucide/svelte';
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

  // One row per device, then host memory, each a meter over what is free right now.
  // With facts on, a device row unfolds what the probes reported about it
  let { host, cpu = false, facts = false }: { host: HostProfile; cpu?: boolean; facts?: boolean } = $props();

  const devices = $derived(host.devices.filter((d) => cpu || d.kind !== DeviceKind.CPU));
  const hostPool = $derived(host.pools.find((p) => p.kind === PoolKind.HOST));

  function poolOf(deviceId: string) {
    return host.pools.find((p) => p.deviceId === deviceId && p.kind !== PoolKind.HOST);
  }
  const rows = $derived.by((): Row[] => {
    const out: Row[] = devices.map((d) => {
      const pool = poolOf(d.id);
      return {
        id: d.id,
        icon: d.kind === DeviceKind.CPU ? Cpu : Microchip,
        name: d.name || d.id,
        sub: `${d.vendor} ${enumLabel(DeviceKind, d.kind)}`.trim(),
        mono: d.id,
        total: pool?.totalBytes ?? d.memoryTotalBytes,
        free: pool?.freeBytes ?? d.memoryFreeBytes,
        facts: facts ? Object.entries(d.facts).sort(([a], [b]) => a.localeCompare(b)) : []
      };
    });
    if (hostPool) out.push({ id: hostPool.id, icon: MemoryStick, name: 'Host memory', sub: 'system RAM', mono: '', total: hostPool.totalBytes, free: hostPool.freeBytes, facts: [] });
    return out;
  });
  const cols = $derived(facts ? 'grid-cols-[auto_minmax(0,16rem)_minmax(0,1fr)_auto_1rem]' : 'grid-cols-[auto_minmax(0,16rem)_minmax(0,1fr)_auto]');
</script>

{#snippet row(r: Row, fold: boolean)}
  {@const Icon = r.icon}
  {@const used = r.total > r.free ? r.total - r.free : 0n}
  <div class="grid {cols} items-center gap-x-5 px-5 py-3.5">
    <Icon size={18} class="text-fg-faint" />
    <div class="min-w-0">
      <div class="truncate text-sm font-medium text-fg" title={r.name}>{r.name}</div>
      <div class="truncate text-xs text-fg-faint">{r.sub}{#if r.mono}{' · '}<span class="font-mono">{r.mono}</span>{/if}</div>
    </div>
    {#if r.total > 0n}
      <Meter value={used} max={r.total} height="md" />
      <div class="w-44 text-right text-sm tabular-nums">
        <span class="text-fg">{bytes(used)}</span>
        <span class="text-fg-faint"> / {bytes(r.total)}</span>
        <span class="ml-2 inline-block w-10 text-right text-fg-muted">{pct(used, r.total).toFixed(0)}%</span>
      </div>
    {:else}
      <div class="col-span-2 text-sm text-fg-faint">No dedicated memory</div>
    {/if}
    {#if facts}
      <span class="flex w-4 justify-center text-fg-faint">{#if fold}<ChevronDown size={15} class="transition-transform group-open:rotate-180" />{/if}</span>
    {/if}
  </div>
{/snippet}

<div class="card divide-y divide-line/70">
  {#each rows as r (r.id)}
    {#if r.facts.length}
      <details class="group">
        <summary class="cursor-pointer list-none transition-colors select-none hover:bg-raised/40 [&::-webkit-details-marker]:hidden">{@render row(r, true)}</summary>
        <div class="border-t border-line/60 bg-sunken/40 px-5 py-4 pl-[3.6rem]">
          <Kv mono columns={2} items={r.facts} />
        </div>
      </details>
    {:else}
      {@render row(r, false)}
    {/if}
  {:else}
    <div class="px-5 py-8 text-center text-sm text-fg-faint">No devices found</div>
  {/each}
</div>
