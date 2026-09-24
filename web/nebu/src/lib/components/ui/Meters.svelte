<script lang="ts" module>
  import type { Component } from 'svelte';
  import type { SizeItem, Units } from './SizeBar.svelte';

  export interface Meter {
    id: string;
    icon: Component<any>;
    name: string;
    total: bigint | number;
    items: SizeItem[];
    units?: Units;
    facts: Record<string, string>;
  }
</script>

<script lang="ts">
  import { ChevronRight } from '@lucide/svelte';
  import { SvelteSet } from 'svelte/reactivity';
  import { bytes, ratioBytes, ratioStorage, storage, type Tone } from '$lib/format';
  import SizeBar from './SizeBar.svelte';
  import FactTable from './FactTable.svelte';

  let { meters, name, bar, empty, unsized }: { meters: Meter[]; name: string; bar: string; empty: string; unsized: string } = $props();

  const open = new SvelteSet<string>();
  const n = (v: bigint | number) => Math.max(Number(v), 0);
  const used = (m: Meter) => m.items.reduce((a, i) => a + n(i.size), 0);
  const free = (m: Meter) => Math.max(0, n(m.total) - used(m));
  const format = (m: Meter) => (m.units === 'decimal' ? storage : bytes);
  const ratio = (m: Meter) => (m.units === 'decimal' ? ratioStorage : ratioBytes);

  const shares = $derived([...new Set(meters.flatMap((m) => m.items.flatMap((i) => (i.label ? [i.label] : []))))]);
  const share = (m: Meter, label: string) => m.items.filter((i) => i.label === label).reduce((a, i) => a + n(i.size), 0);
  const tone = (label: string): Tone => meters.flatMap((m) => m.items).find((i) => i.label === label)?.tone ?? 'accent';
  const dots: Record<Tone, string> = { ok: 'text-ok', warn: 'text-warn', bad: 'text-bad', info: 'text-info', accent: 'text-accent', neutral: 'text-fg-faint' };
  const columns = $derived(4 + shares.length);

  function toggle(id: string) {
    if (open.has(id)) open.delete(id);
    else open.add(id);
  }
</script>

<div class="tbl-wrap @container">
  <table class="tbl table-fixed">
    <thead>
      <tr>
        <th class="w-56 @3xl:w-72 @5xl:w-96">{name}</th>
        <th>{bar}</th>
        <th class="num w-28">Used</th>
        <th class="num w-24">Free</th>
        {#each shares as s (s)}<th class="num w-28">{s}</th>{/each}
      </tr>
    </thead>
    <tbody>
      {#each meters as m (m.id)}
        {@const Icon = m.icon}
        {@const sized = n(m.total) > 0}
        {@const fold = Object.keys(m.facts).length > 0}
        {@const opened = open.has(m.id)}
        <tr class="{fold ? 'row-link' : ''} {opened ? 'row-active' : ''}" onclick={fold ? () => toggle(m.id) : undefined}>
          <td>
            <div class="flex items-center gap-2">
              <span class="flex w-3 shrink-0 justify-center">{#if fold}<ChevronRight size={12} class="text-fg-faint transition-transform {opened ? 'rotate-90' : ''}" />{/if}</span>
              <Icon size={16} class="shrink-0 text-fg-faint" />
              <span class="min-w-0 truncate font-medium text-fg" title={m.name}>{m.name}</span>
            </div>
          </td>
          <td>
            {#if sized}
              <SizeBar dense total={m.total} units={m.units} overlays={[{ start: 'left', items: m.items }]} />
            {:else}
              <span class="text-fg-faint">{unsized}</span>
            {/if}
          </td>
          <td class="num">
            {#if sized}<span title="{format(m)(used(m))} of {format(m)(m.total)}">{ratio(m)(used(m), m.total)}</span>{:else}<span class="text-fg-faint">–</span>{/if}
          </td>
          <td class="num">{#if sized}{format(m)(free(m))}{:else}<span class="text-fg-faint">–</span>{/if}</td>
          {#each shares as s (s)}
            {@const v = share(m, s)}
            <td class="num">
              {#if v > 0}<span class="inline-flex items-center gap-1.5"><span class="dot {dots[tone(s)]}"></span>{format(m)(v)}</span>{:else}<span class="text-fg-faint">–</span>{/if}
            </td>
          {/each}
        </tr>
        {#if opened}
          <tr>
            <td colspan={columns} class="bg-sunken/40 !py-3 !pr-4 !pl-12">
              <FactTable class="max-w-3xl" facts={m.facts} columns={2} />
            </td>
          </tr>
        {/if}
      {:else}
        <tr><td colspan={columns} class="text-fg-faint">{empty}</td></tr>
      {/each}
    </tbody>
  </table>
</div>
