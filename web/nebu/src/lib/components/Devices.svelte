<script lang="ts">
  import type { Component } from 'svelte';
  import { bytes, pct, enumLabel } from '$lib/format';
  import { DeviceKind, PoolKind, type HostProfile, type MemoryPool } from '$proto/host_pb';
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
    // Others draw on the same pool, unified memory or a second socket
    shared: boolean;
    facts: [string, string][];
  }

  // One row per device with the memory it draws on: a card's own pool, the system memory for a CPU, one unified pool for both on Apple silicon
  let { host }: { host: HostProfile } = $props();

  const rows = $derived.by((): Row[] => {
    const owned = new Map<string, MemoryPool>();
    for (const p of host.pools) if (p.deviceId) owned.set(p.deviceId, p);
    const loose = host.pools.filter((p) => !p.deviceId);
    const system = loose.find((p) => p.kind === PoolKind.HOST);
    const unified = loose.find((p) => p.kind === PoolKind.UNIFIED);
    const users = new Map<string, number>();
    const poolOf = (d: HostProfile['devices'][number]): MemoryPool | undefined => {
      const own = owned.get(d.id);
      if (own) return own;
      if (d.kind === DeviceKind.CPU) return system ?? unified;
      return unified;
    };
    for (const d of host.devices) {
      const p = poolOf(d);
      if (p) users.set(p.id, (users.get(p.id) ?? 0) + 1);
    }
    const out: Row[] = host.devices.map((d) => {
      const pool = poolOf(d);
      const shared = !!pool && (users.get(pool.id) ?? 0) > 1;
      const parts = [d.vendor, enumLabel(DeviceKind, d.kind)].filter(Boolean);
      if (pool && !owned.has(d.id)) parts.push(pool.kind === PoolKind.UNIFIED ? 'unified memory' : 'system memory');
      if (shared) parts.push('shared');
      return {
        id: d.id,
        icon: d.kind === DeviceKind.CPU ? Cpu : Microchip,
        name: d.name || d.id,
        sub: parts.join(' · '),
        mono: d.id,
        total: pool?.totalBytes ?? d.memoryTotalBytes,
        free: pool?.freeBytes ?? d.memoryFreeBytes,
        shared,
        facts: Object.entries(d.facts).sort(([a], [b]) => a.localeCompare(b))
      };
    });
    // Memory probed with no processor probed to hang it on still gets a row
    for (const p of loose) {
      if ((users.get(p.id) ?? 0) === 0) {
        out.push({ id: p.id, icon: MemoryStick, name: p.kind === PoolKind.UNIFIED ? 'Unified memory' : 'System memory', sub: enumLabel(PoolKind, p.kind), mono: p.id, total: p.totalBytes, free: p.freeBytes, shared: false, facts: [] });
      }
    }
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
      <div class="col-span-2 text-sm text-fg-faint">Memory not probed</div>
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
    <div class="px-2 py-6 text-center text-sm text-fg-faint">No devices probed</div>
  {/each}
</div>
