<script lang="ts">
  import { Tooltip } from 'bits-ui';
  import type { Snippet } from 'svelte';

  let { children, content, text, class: cls = '' }: { children: Snippet; content?: Snippet; text?: string; class?: string } = $props();
</script>

<Tooltip.Root>
  <Tooltip.Trigger>
    {#snippet child({ props })}
      <span {...props} class="inline-flex {cls}">{@render children()}</span>
    {/snippet}
  </Tooltip.Trigger>
  <Tooltip.Portal>
    <Tooltip.Content side="top" sideOffset={6} class="fade z-[90] max-w-xs rounded-lg border border-line bg-overlay px-3 py-2 text-xs leading-5 text-fg shadow-pop">
      {#if content}{@render content()}{:else}{text}{/if}
    </Tooltip.Content>
  </Tooltip.Portal>
</Tooltip.Root>
