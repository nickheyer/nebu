<script lang="ts">
  import type { Snippet } from 'svelte';

  // One number with its name above and a word about it beneath
  let { label, value, sub, title, children }: { label: string; value: string; sub?: string; title?: string; children?: Snippet } = $props();
</script>

<div class="flex min-w-0 flex-col gap-1">
  <div class="text-xs font-medium text-fg-muted">{label}</div>
  <div class="truncate text-2xl leading-tight font-semibold tabular-nums text-fg" title={title ?? value}>{value}</div>
  {#if sub}<div class="truncate text-xs text-fg-faint" title={sub}>{sub}</div>{/if}
  {@render children?.()}
</div>
