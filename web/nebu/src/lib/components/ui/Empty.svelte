<script lang="ts">
  import type { Component, Snippet } from 'svelte';

  let { icon, title, description, children, compact = false, class: cls = '' }: { icon?: Component<any>; title: string; description?: string; children?: Snippet; compact?: boolean; class?: string } = $props();
</script>

<div class="flex flex-col items-center justify-center rounded-md border border-dashed border-line text-center {compact ? 'px-4 py-8' : 'px-6 py-14'} {cls}">
  {#if icon}
    {@const Icon = icon}
    <Icon size={18} class="mb-3 text-fg-faint" />
  {/if}
  <div class="text-sm text-fg-muted [overflow-wrap:anywhere]">{title}</div>
  {#if description}<p class="mt-1.5 max-w-md text-sm leading-6 text-fg-faint [overflow-wrap:anywhere]">{description}</p>{/if}
  {#if children}<div class="mt-4 flex flex-wrap items-center justify-center gap-2">{@render children()}</div>{/if}
</div>
