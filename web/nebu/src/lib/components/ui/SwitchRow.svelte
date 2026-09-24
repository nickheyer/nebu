<script lang="ts">
  import Switch from './Switch.svelte';

  // A labeled switch in a bordered row, the same height as a text input when it has no description.
  let { checked = $bindable(false), label, description, disabled = false, onchange }: { checked?: boolean; label: string; description?: string; disabled?: boolean; onchange?: (checked: boolean) => void } = $props();
</script>

<div class="flex min-h-9 items-center justify-between gap-4 rounded-md border border-line px-3 py-1.5 {disabled ? 'opacity-60' : ''}">
  <div class="flex min-w-0 flex-col py-0.5">
    <span class="text-[13px] leading-5 font-medium text-fg">{label}</span>
    {#if description}<span class="text-xs leading-5 text-fg-muted">{description}</span>{/if}
  </div>
  <Switch bind:checked {label} {disabled} {onchange} />
</div>
