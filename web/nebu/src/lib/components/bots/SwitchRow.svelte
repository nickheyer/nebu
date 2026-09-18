<script lang="ts">
  import Switch from '../ui/Switch.svelte';

  let { checked = $bindable(false), label, description, disabled = false, onchange }: { checked?: boolean; label: string; description?: string; disabled?: boolean; onchange?: (checked: boolean) => void } = $props();
</script>

<div class="flex items-center justify-between gap-4 rounded-md border border-line px-3 py-2.5 {disabled ? 'opacity-60' : ''}">
  <div class="flex min-w-0 flex-col">
    <span class="text-[13px] font-medium text-fg">{label}</span>
    {#if description}<span class="text-xs leading-5 text-fg-muted">{description}</span>{/if}
  </div>
  <Switch bind:checked {label} {disabled} {onchange} />
</div>
