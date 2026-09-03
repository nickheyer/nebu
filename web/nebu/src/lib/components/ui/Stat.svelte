<script lang="ts">
  import type { Snippet } from 'svelte';

  let { label, value, sub, children, class: cls = '' }: { label: string; value?: string; sub?: string; children?: Snippet; class?: string } = $props();
</script>

<div class="flex min-w-0 flex-col gap-0.5 {cls}">
  <div class="text-[11px] font-semibold tracking-wider text-fg-faint uppercase">{label}</div>
  {#if value !== undefined}<div class="truncate text-lg font-semibold tabular-nums text-fg">{value}</div>{/if}
  {@render children?.()}
  {#if sub}<div class="truncate text-xs text-fg-muted">{sub}</div>{/if}
</div>
