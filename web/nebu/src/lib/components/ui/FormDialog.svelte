<script lang="ts">
  import type { Component, Snippet } from 'svelte';
  import Dialog from './Dialog.svelte';
  import Button from './Button.svelte';

  let {
    open = $bindable(false),
    title,
    subtitle,
    size = 'md',
    action,
    icon,
    saving = false,
    disabled = false,
    note = '',
    onsubmit,
    children
  }: {
    open?: boolean;
    title: string;
    subtitle?: string;
    size?: 'sm' | 'md' | 'lg' | 'xl';
    // The primary button's label
    action: string;
    icon?: Component<any>;
    saving?: boolean;
    disabled?: boolean;
    // A short warning beside the buttons, such as what is still missing
    note?: string;
    onsubmit: () => void;
    children: Snippet;
  } = $props();
</script>

<Dialog bind:open {title} {subtitle} {size}>
  <form
    onsubmit={(e) => {
      e.preventDefault();
      if (!disabled && !saving) onsubmit();
    }}
  >
    {@render children()}
    <button type="submit" class="hidden" aria-hidden="true" tabindex="-1"></button>
  </form>
  {#snippet footer()}
    {#if note}<span class="mr-auto text-sm text-warn">{note}</span>{/if}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button variant="primary" {icon} loading={saving} {disabled} onclick={onsubmit}>{action}</Button>
  {/snippet}
</Dialog>
