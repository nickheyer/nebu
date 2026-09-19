<script lang="ts">
  import type { Snippet } from 'svelte';

  let { title, meta, actions, children, padded = true, class: cls = '' }: { title?: string; meta?: string; actions?: Snippet; children: Snippet; padded?: boolean; class?: string } = $props();
</script>

<section class="card flex flex-col {cls}">
  {#if title || actions}
    <header class="flex min-h-11 items-center gap-2.5 border-b border-line px-4">
      {#if title}<h2 class="text-sm font-semibold text-fg">{title}</h2>{/if}
      {#if meta}<span class="truncate text-sm text-fg-faint">{meta}</span>{/if}
      {#if actions}<div class="ml-auto flex shrink-0 items-center gap-2">{@render actions()}</div>{/if}
    </header>
  {/if}
  <div class={padded ? 'p-4' : ''}>{@render children()}</div>
</section>
