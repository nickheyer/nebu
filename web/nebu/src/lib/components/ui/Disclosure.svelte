<script lang="ts">
  import type { Snippet } from 'svelte';
  import { ChevronRight } from '@lucide/svelte';

  // A row that folds its content away, the summary saying what is inside while closed
  let {
    label,
    summary,
    open = $bindable(false),
    tone = 'default',
    children,
    class: cls = ''
  }: { label: string; summary?: string; open?: boolean; tone?: 'default' | 'warn'; children: Snippet; class?: string } = $props();
</script>

<div class="flex flex-col {cls}">
  <button type="button" class="flex h-8 w-full items-center gap-2 rounded-md px-1 text-left text-sm transition-colors hover:bg-raised/60" aria-expanded={open} onclick={() => (open = !open)}>
    <ChevronRight size={14} class="shrink-0 text-fg-faint transition-transform {open ? 'rotate-90' : ''}" />
    <span class="font-medium {tone === 'warn' ? 'text-warn' : 'text-fg'}">{label}</span>
    {#if summary}<span class="truncate text-fg-faint">{summary}</span>{/if}
  </button>
  {#if open}
    <div class="pt-2 pl-6">{@render children()}</div>
  {/if}
</div>
