<script lang="ts">
  import type { Snippet } from 'svelte';
  import { ArrowLeft } from '@lucide/svelte';

  // The top of every page: where it sits, what it is, the facts beside it, and the actions that belong to the whole page
  let { title, mono = false, back, meta, children, below }: { title: string; mono?: boolean; back?: { href: string; label: string }; meta?: Snippet; children?: Snippet; below?: Snippet } = $props();
</script>

<header class="mb-6 flex flex-col gap-4">
  {#if back}
    <a href={back.href} class="inline-flex w-fit items-center gap-1.5 text-sm text-fg-muted transition-colors hover:text-fg"><ArrowLeft size={14} />{back.label}</a>
  {/if}
  <div class="flex flex-wrap items-center gap-x-5 gap-y-3">
    <h1 class="text-[22px] leading-8 font-semibold text-fg {mono ? 'font-mono' : ''}">{title}</h1>
    {#if meta}<div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-sm text-fg-muted">{@render meta()}</div>{/if}
    {#if children}<div class="ml-auto flex shrink-0 flex-wrap items-center gap-2">{@render children()}</div>{/if}
  </div>
  {#if below}{@render below()}{/if}
</header>
