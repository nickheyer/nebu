<script lang="ts">
  let { facts, mono = true, columns = 1, class: cls = '' }: { facts: Record<string, string> | [string, string][]; mono?: boolean; columns?: number; class?: string } = $props();

  // Multiline values also count as long.
  const short = 24;

  interface Row {
    key: string;
    shown: string;
    depth: number;
    value: string;
    numeric: boolean;
    long: boolean;
    group?: string;
  }

  const entries = $derived((Array.isArray(facts) ? facts : Object.entries(facts)).map(([k, v]) => [k, String(v ?? '')] as [string, string]).sort(([a], [b]) => a.localeCompare(b)));
  // Group keys with shared prefixes under a heading.
  const rows = $derived.by(() => {
    const prefixes = new Map<string, number>();
    for (const [k] of entries) {
      const i = k.lastIndexOf('.');
      if (i > 0) prefixes.set(k.slice(0, i), (prefixes.get(k.slice(0, i)) ?? 0) + 1);
    }
    const out: Row[] = [];
    let last = '';
    for (const [k, v] of entries) {
      const i = k.lastIndexOf('.');
      const prefix = i > 0 && (prefixes.get(k.slice(0, i)) ?? 0) > 1 ? k.slice(0, i) : '';
      if (prefix && prefix !== last) out.push({ key: prefix, shown: prefix, depth: 0, value: '', numeric: false, long: false, group: prefix });
      last = prefix;
      out.push({ key: k, shown: prefix ? k.slice(prefix.length + 1) : k, depth: prefix ? 1 : 0, value: v, numeric: /^-?\d+(\.\d+)?$/.test(v.trim()), long: v.length > short || v.includes('\n') });
    }
    return out;
  });
  // Omit headings with only long values. Those values show their full keys below.
  const flowing = $derived(rows.filter((r, i) => !r.long && (!r.group || rows.slice(i + 1).some((n) => !n.long && n.key.startsWith(r.group + '.')))));
  const wide = $derived(rows.filter((r) => r.long).map((r) => ({ ...r, shown: r.key, depth: 0 })));
  // Include indentation when measuring key width.
  const keyWidth = $derived(Math.min(32, Math.max(4, ...[...flowing, ...wide].filter((r) => !r.group).map((r) => r.shown.length + r.depth * 2))));
</script>

{#snippet fact(r: Row)}
  <div class="flex gap-4 break-inside-avoid">
    <span class="shrink-0 text-fg-muted wrap-anywhere" style="width: {keyWidth}ch; padding-left: {r.depth * 2}ch" title={r.key}>{r.shown}</span>
    <span class="min-w-0 flex-1 wrap-anywhere whitespace-pre-wrap {r.value === '' ? 'text-fg-faint' : 'text-fg'} {r.numeric ? 'tabular-nums' : ''}">{r.value === '' ? '–' : r.value}</span>
  </div>
{/snippet}

{#if entries.length === 0}
  <p class="text-sm text-fg-faint">Nothing probed.</p>
{:else if columns > 1}
  <div class="flex flex-col gap-2 {mono ? 'font-mono text-xs' : 'text-sm'} {cls}">
    {#if flowing.length}
      <div class="space-y-2" style="columns: 18rem {columns}; column-gap: 3rem">
        {#each flowing as r (r.key)}
          {#if r.group}
            <div class="caps pt-2 break-after-avoid text-fg-faint">{r.group}</div>
          {:else}
            {@render fact(r)}
          {/if}
        {/each}
      </div>
    {/if}
    {#each wide as r (r.key)}
      {@render fact(r)}
    {/each}
  </div>
{:else}
  <table class="tbl dense {cls}">
    <tbody>
      {#each rows as r (r.key)}
        {#if r.group}
          <tr><td colspan="2" class="caps !pt-3 text-fg-faint">{r.group}</td></tr>
        {:else}
          <tr>
            <td class="w-px whitespace-nowrap align-top text-fg-muted {mono ? 'font-mono text-xs' : ''}" style="padding-left: {0.5 + r.depth * 1.25}rem" title={r.key}>{r.shown}</td>
            <td class="align-top {mono ? 'font-mono text-xs' : ''}">
              <span class="wrap-anywhere whitespace-pre-wrap {r.value === '' ? 'text-fg-faint' : 'text-fg'} {r.numeric ? 'tabular-nums' : ''}">{r.value === '' ? '–' : r.value}</span>
            </td>
          </tr>
        {/if}
      {/each}
    </tbody>
  </table>
{/if}
