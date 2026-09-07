<script lang="ts">
  let {
    checked = $bindable(false),
    label,
    hint,
    onchange,
    disabled = false,
    class: cls = ''
  }: { checked?: boolean; label: string; hint?: string; onchange?: (checked: boolean) => void; disabled?: boolean; class?: string } = $props();
</script>

<label class="inline-flex min-h-8 cursor-pointer items-start gap-2.5 py-1.5 text-sm text-fg {disabled ? 'opacity-50' : ''} {cls}">
  <input type="checkbox" class="checkbox mt-0.5" bind:checked {disabled} onchange={(e) => onchange?.(e.currentTarget.checked)} />
  <span class="flex flex-col">
    <span>{label}</span>
    {#if hint}<span class="text-xs text-fg-faint">{hint}</span>{/if}
  </span>
</label>
