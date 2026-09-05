<script lang="ts">
  import type { Snippet } from 'svelte';
  import { ChevronRight } from '@lucide/svelte';
  import Info from './Info.svelte';

  // A bordered surface with an optional header. Flush cards hold a table or a list edge to edge
  let {
    title,
    description,
    info,
    href,
    actions,
    children,
    flush = false,
    class: cls = ''
  }: { title?: string; description?: string; info?: string; href?: string; actions?: Snippet; children: Snippet; flush?: boolean; class?: string } = $props();
</script>

<section class="card flex flex-col overflow-hidden {cls}">
  {#if title || actions}
    <header class="flex min-h-14 items-center gap-3 border-b border-line px-5 py-3">
      <div class="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
        {#if title && href}
          <a {href} class="group inline-flex items-center gap-1 text-base font-semibold text-fg hover:text-accent"><h2>{title}</h2><ChevronRight size={16} class="text-fg-faint transition-colors group-hover:text-accent" /></a>
        {:else if title}
          <h2 class="text-base font-semibold text-fg">{title}</h2>
        {/if}
        {#if description}<span class="truncate text-sm text-fg-muted">{description}</span>{/if}
        {#if info}<Info text={info} />{/if}
      </div>
      {#if actions}<div class="flex shrink-0 items-center gap-2">{@render actions()}</div>{/if}
    </header>
  {/if}
  <div class={flush ? '' : 'p-5'}>
    {@render children()}
  </div>
</section>
