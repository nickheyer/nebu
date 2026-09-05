<script lang="ts">
  import type { Snippet } from 'svelte';
  import Info from './Info.svelte';

  // A labeled control, the one explanation behind an info mark and any error beneath
  let {
    label,
    info,
    error,
    for: id,
    children,
    trailing,
    class: cls = ''
  }: { label: string; info?: string; error?: string; for?: string; children: Snippet; trailing?: Snippet; class?: string } = $props();
</script>

<div class="flex min-w-0 flex-col gap-1.5 {cls}">
  <div class="flex h-5 items-center gap-1">
    <label for={id} class="caps text-fg-faint">{label}</label>
    {#if info}<Info text={info} />{/if}
    {#if trailing}<span class="ml-auto">{@render trailing()}</span>{/if}
  </div>
  {@render children()}
  {#if error}<p class="text-xs text-bad">{error}</p>{/if}
</div>
