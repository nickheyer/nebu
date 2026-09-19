<script lang="ts">
  let {
    items,
    mono = false,
    columns = 1,
    omitEmpty = false,
    class: cls = ''
  }: { items: [string, string | number | undefined | null][]; mono?: boolean; columns?: 1 | 2 | 3; omitEmpty?: boolean; class?: string } = $props();
  const blank = (v: string | number | undefined | null) => v === undefined || v === null || v === '';
  const shown = $derived(omitEmpty ? items.filter(([, v]) => !blank(v)) : items);
  const grid = { 1: 'grid-cols-[auto_1fr]', 2: 'grid-cols-[auto_1fr] sm:grid-cols-[auto_1fr_auto_1fr]', 3: 'grid-cols-[auto_1fr] sm:grid-cols-[auto_1fr_auto_1fr] xl:grid-cols-[auto_1fr_auto_1fr_auto_1fr]' };
</script>

<dl class="grid gap-x-6 gap-y-2 text-sm {grid[columns]} {cls}">
  {#each shown as [k, v] (k)}
    <dt class="whitespace-nowrap text-fg-faint">{k}</dt>
    <dd class="min-w-0 truncate text-fg {mono ? 'mono' : ''}" title={String(v ?? '')}>{blank(v) ? '–' : v}</dd>
  {/each}
</dl>
