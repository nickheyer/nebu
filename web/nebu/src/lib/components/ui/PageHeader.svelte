<script lang="ts">
  import type { Snippet } from 'svelte';

  // The top of every page: what it is, the facts beside it, and the actions that belong to the whole page
  let { title, mono = false, meta, children, below }: { title: string; mono?: boolean; meta?: Snippet; children?: Snippet; below?: Snippet } = $props();
</script>

<header class="mb-7 flex flex-col gap-4">
  <div class="flex flex-wrap items-center gap-x-5 gap-y-3">
    <h1 class="text-[22px] leading-8 font-semibold text-fg {mono ? 'font-mono' : ''}">{title}</h1>
    {#if meta}<div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-sm text-fg-muted">{@render meta()}</div>{/if}
    {#if children}<div class="ml-auto flex shrink-0 flex-wrap items-center gap-2">{@render children()}</div>{/if}
  </div>
  {#if below}{@render below()}{/if}
</header>
