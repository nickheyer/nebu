<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title,
    description,
    children,
    meta
  }: { title: string; description?: string; children?: Snippet; meta?: Snippet } = $props();
</script>

<header class="mb-6 flex flex-wrap items-end gap-4">
  <div class="min-w-0">
    <h1 class="text-xl font-semibold tracking-tight text-fg">{title}</h1>
    {#if description}<p class="mt-1 text-sm text-fg-muted">{description}</p>{/if}
    {#if meta}<div class="mt-2 flex flex-wrap items-center gap-2 text-xs text-fg-muted">{@render meta()}</div>{/if}
  </div>
  {#if children}<div class="ml-auto flex shrink-0 flex-wrap items-center gap-2">{@render children()}</div>{/if}
</header>
