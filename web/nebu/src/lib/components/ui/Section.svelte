<script lang="ts">
  import type { Snippet } from 'svelte';

  let { title, actions, children }: { title: string; actions?: Snippet; children: Snippet } = $props();
</script>

<section>
  <div class="mb-2 flex flex-wrap items-center gap-3">
    <h3 class="text-[11px] font-semibold tracking-wider text-fg-faint uppercase">{title}</h3>
    {@render actions?.()}
  </div>
  {@render children()}
</section>
