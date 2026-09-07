<script lang="ts">
  import type { Snippet } from 'svelte';

  // A labeled control, a hint beneath it and any error in its place
  let {
    label,
    hint,
    error,
    for: id,
    children,
    trailing,
    class: cls = ''
  }: { label: string; hint?: string; error?: string; for?: string; children: Snippet; trailing?: Snippet; class?: string } = $props();
</script>

<div class="flex min-w-0 flex-col gap-1.5 {cls}">
  <div class="flex h-5 items-center gap-1">
    <label for={id} class="caps text-fg-faint">{label}</label>
    {#if trailing}<span class="ml-auto">{@render trailing()}</span>{/if}
  </div>
  {@render children()}
  {#if error}<p class="text-xs text-bad">{error}</p>{:else if hint}<p class="text-xs text-fg-faint">{hint}</p>{/if}
</div>
