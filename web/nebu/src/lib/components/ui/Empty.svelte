<script lang="ts">
  import type { Component, Snippet } from 'svelte';

  let {
    icon,
    title,
    description,
    children,
    compact = false
  }: { icon?: Component<any>; title: string; description?: string; children?: Snippet; compact?: boolean } = $props();
</script>

<div class="flex flex-col items-center justify-center text-center {compact ? 'px-4 py-10' : 'px-6 py-16'}">
  {#if icon}
    {@const Icon = icon}
    <div class="mb-4 flex h-11 w-11 items-center justify-center rounded-full border border-line bg-raised text-fg-muted">
      <Icon size={20} />
    </div>
  {/if}
  <div class="max-w-md text-base font-medium break-words text-fg">{title}</div>
  {#if description}<p class="mt-1 max-w-md text-sm leading-6 break-words text-fg-muted">{description}</p>{/if}
  {#if children}<div class="mt-5 flex flex-wrap items-center justify-center gap-2">{@render children()}</div>{/if}
</div>
