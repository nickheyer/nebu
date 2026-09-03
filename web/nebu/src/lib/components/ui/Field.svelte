<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    label,
    hint,
    for: id,
    children,
    class: cls = ''
  }: { label: string; hint?: string; for?: string; children: Snippet; class?: string } = $props();
</script>

<div class="flex flex-col gap-1.5 {cls}">
  <label for={id} class="text-xs font-medium text-fg-muted">{label}</label>
  {@render children()}
  {#if hint}<p class="text-[11.5px] leading-4 text-fg-faint">{hint}</p>{/if}
</div>
