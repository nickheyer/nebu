<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title,
    description,
    actions,
    children,
    flush = false,
    class: cls = ''
  }: { title?: string; description?: string; actions?: Snippet; children: Snippet; flush?: boolean; class?: string } = $props();
</script>

<section class="panel flex flex-col overflow-hidden {cls}">
  {#if title || actions}
    <header class="flex items-center gap-3 border-b border-line px-4 py-3">
      <div class="min-w-0">
        {#if title}<h2 class="text-sm font-semibold text-fg">{title}</h2>{/if}
        {#if description}<p class="text-xs text-fg-muted">{description}</p>{/if}
      </div>
      {#if actions}<div class="ml-auto flex shrink-0 items-center gap-2">{@render actions()}</div>{/if}
    </header>
  {/if}
  <div class={flush ? '' : 'p-4'}>
    {@render children()}
  </div>
</section>
