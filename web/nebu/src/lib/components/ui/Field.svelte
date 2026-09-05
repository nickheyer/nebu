<script lang="ts">
  import type { Snippet } from 'svelte';
  import Info from './Info.svelte';

  // A labeled control. The info mark beside the label holds the one short explanation, the hint sits beneath
  let {
    label,
    hint,
    info,
    error,
    for: id,
    children,
    class: cls = ''
  }: { label: string; hint?: string; info?: string; error?: string; for?: string; children: Snippet; class?: string } = $props();
</script>

<div class="flex flex-col gap-1.5 {cls}">
  <div class="flex h-5 items-center gap-1">
    <label for={id} class="text-sm font-medium text-fg-muted">{label}</label>
    {#if info}<Info text={info} />{/if}
  </div>
  {@render children()}
  {#if error}
    <p class="text-xs text-bad">{error}</p>
  {:else if hint}
    <p class="text-xs leading-4 text-fg-faint">{hint}</p>
  {/if}
</div>
