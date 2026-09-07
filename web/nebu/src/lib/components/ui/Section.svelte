<script lang="ts">
  import type { Snippet } from 'svelte';

  // A titled block of a page: the title, a count, a word of context, the actions, then the content
  let {
    title,
    count,
    meta,
    actions,
    children,
    id,
    class: cls = ''
  }: { title: string; count?: number; meta?: string; actions?: Snippet; children: Snippet; id?: string; class?: string } = $props();
</script>

<section {id} class="flex flex-col gap-3 {cls}">
  <header class="flex min-h-8 items-center gap-2.5">
    <h2 class="text-sm font-semibold text-fg">{title}</h2>
    {#if count !== undefined}<span class="text-sm tabular-nums text-fg-faint">{count}</span>{/if}
    {#if meta}<span class="truncate text-sm text-fg-faint">{meta}</span>{/if}
    {#if actions}<div class="ml-auto flex shrink-0 items-center gap-2">{@render actions()}</div>{/if}
  </header>
  {@render children()}
</section>
