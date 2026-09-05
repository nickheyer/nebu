<script lang="ts">
  import { Tooltip } from 'bits-ui';
  import type { Snippet } from 'svelte';

  let {
    children,
    content,
    text,
    side = 'top',
    class: cls = ''
  }: { children: Snippet; content?: Snippet; text?: string; side?: 'top' | 'bottom' | 'left' | 'right'; class?: string } = $props();
</script>

{#if !text && !content}
  <span class="inline-flex {cls}">{@render children()}</span>
{:else}
  <Tooltip.Root>
    <Tooltip.Trigger>
      {#snippet child({ props })}
        <span {...props} class="inline-flex {cls}">{@render children()}</span>
      {/snippet}
    </Tooltip.Trigger>
    <Tooltip.Portal>
      <Tooltip.Content {side} sideOffset={6} class="fade z-[90] max-w-xs rounded-md border border-line bg-overlay px-2.5 py-1.5 text-xs leading-5 text-fg shadow-pop">
        {#if content}{@render content()}{:else}{text}{/if}
      </Tooltip.Content>
    </Tooltip.Portal>
  </Tooltip.Root>
{/if}
