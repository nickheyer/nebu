<script lang="ts">
  import { Tooltip } from 'bits-ui';
  import type { Snippet } from 'svelte';

  let { children, content, text, side = 'top' }: { children: Snippet; content?: Snippet; text?: string; side?: 'top' | 'bottom' | 'left' | 'right' } = $props();
</script>

<Tooltip.Root>
  <Tooltip.Trigger>
    {#snippet child({ props })}
      <span {...props} class="inline-flex">{@render children()}</span>
    {/snippet}
  </Tooltip.Trigger>
  <Tooltip.Portal>
    <Tooltip.Content {side} sideOffset={6} class="fade z-[90] max-w-xs rounded-md border border-line bg-overlay px-2.5 py-1.5 text-xs text-fg shadow-pop">
      {#if content}{@render content()}{:else}{text}{/if}
    </Tooltip.Content>
  </Tooltip.Portal>
</Tooltip.Root>
