<script lang="ts">
  import type { Component, Snippet } from 'svelte';
  import Dialog from './Dialog.svelte';
  import Button from './Button.svelte';

  let {
    open = $bindable(false),
    title,
    description,
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
    description?: string;
    size?: 'sm' | 'md' | 'lg' | 'xl';
    // The primary button's label
    action: string;
    icon?: Component<any>;
    saving?: boolean;
    disabled?: boolean;
    // A warning shown beside the buttons, such as what is still missing
    note?: string;
    onsubmit: () => void;
    children: Snippet;
  } = $props();
</script>

<Dialog bind:open {title} {description} {size}>
  {@render children()}
  {#snippet footer()}
    {#if note}<span class="mr-auto text-xs text-warn">{note}</span>{/if}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button variant="primary" {icon} loading={saving} {disabled} onclick={onsubmit}>{action}</Button>
  {/snippet}
</Dialog>
