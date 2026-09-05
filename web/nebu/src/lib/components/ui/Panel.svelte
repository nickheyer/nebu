<script lang="ts">
  import type { Snippet } from 'svelte';

  import { ChevronRight } from '@lucide/svelte';
  import Info from './Info.svelte';

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

<section class="panel flex flex-col overflow-hidden {cls}">
  {#if title || actions || info}
    <header class="flex items-center gap-3 border-b border-line px-4 py-3">
      <div class="flex min-w-0 items-baseline gap-2">
        {#if title && href}
          <a {href} class="group inline-flex items-center gap-1 text-sm font-semibold text-fg hover:text-accent"><h2>{title}</h2><ChevronRight size={14} class="text-fg-faint transition-colors group-hover:text-accent" /></a>
        {:else if title}<h2 class="text-sm font-semibold text-fg">{title}</h2>{/if}
        {#if description}<span class="text-xs text-fg-faint">{description}</span>{/if}
      </div>
      <div class="ml-auto flex shrink-0 items-center gap-2">
        {#if actions}{@render actions()}{/if}
        {#if info}<Info text={info} />{/if}
      </div>
    </header>
  {/if}
  <div class={flush ? '' : 'p-4'}>
    {@render children()}
  </div>
</section>
