<script lang="ts">
  import type { Snippet } from 'svelte';

  // The top of every page: what it is, a line about it, and the actions that belong to the whole page
  let { title, subtitle, children, meta }: { title: string; subtitle?: string; children?: Snippet; meta?: Snippet } = $props();
</script>

<header class="mb-8 flex flex-wrap items-start gap-4">
  <div class="min-w-0 flex-1">
    <h1 class="text-xl font-semibold text-fg">{title}</h1>
    {#if subtitle}<p class="mt-1 text-sm text-fg-muted">{subtitle}</p>{/if}
    {#if meta}<div class="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-fg-muted">{@render meta()}</div>{/if}
  </div>
  {#if children}<div class="flex shrink-0 flex-wrap items-center gap-2">{@render children()}</div>{/if}
</header>
