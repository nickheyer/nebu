<script lang="ts">
  import type { Snippet } from 'svelte';

  // A titled block inside a panel or dialog
  let { title, description, actions, children }: { title: string; description?: string; actions?: Snippet; children: Snippet } = $props();
</script>

<section>
  <div class="mb-2.5 flex flex-wrap items-center gap-3">
    <h3 class="text-sm font-semibold text-fg">{title}</h3>
    {#if description}<span class="text-xs text-fg-faint">{description}</span>{/if}
    {#if actions}<div class="ml-auto flex items-center gap-2">{@render actions()}</div>{/if}
  </div>
  {@render children()}
</section>
