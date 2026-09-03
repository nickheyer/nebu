<script lang="ts">
  let {
    items,
    mono = false,
    columns = 1
  }: { items: [string, string | number | undefined | null][]; mono?: boolean; columns?: 1 | 2 } = $props();
</script>

<dl class="grid gap-x-6 gap-y-2 text-sm {columns === 2 ? 'grid-cols-[auto_1fr_auto_1fr]' : 'grid-cols-[auto_1fr]'}">
  {#each items as [k, v] (k)}
    <dt class="text-fg-faint whitespace-nowrap">{k}</dt>
    <dd class="min-w-0 truncate text-fg {mono ? 'mono' : ''}" title={String(v ?? '')}>{v === undefined || v === null || v === '' ? '–' : v}</dd>
  {/each}
</dl>
