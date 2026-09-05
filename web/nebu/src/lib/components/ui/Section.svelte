<script lang="ts">
  import type { Snippet } from 'svelte';
  import Info from './Info.svelte';

  // A titled block of a page: the title, a count, a word of context, the actions, then the content
  let {
    title,
    count,
    meta,
    info,
    actions,
    children,
    id,
    class: cls = ''
  }: { title: string; count?: number; meta?: string; info?: string; actions?: Snippet; children: Snippet; id?: string; class?: string } = $props();
</script>

<section {id} class="flex flex-col gap-3 {cls}">
  <header class="flex min-h-8 items-center gap-2.5">
    <h2 class="text-sm font-semibold text-fg">{title}</h2>
    {#if count !== undefined}<span class="text-sm tabular-nums text-fg-faint">{count}</span>{/if}
    {#if meta}<span class="truncate text-sm text-fg-faint">{meta}</span>{/if}
    <div class="ml-auto flex shrink-0 items-center gap-2">
      {#if actions}{@render actions()}{/if}
      {#if info}<Info text={info} />{/if}
    </div>
  </header>
  {@render children()}
</section>
