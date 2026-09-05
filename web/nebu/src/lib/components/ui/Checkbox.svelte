<script lang="ts">
  let {
    checked = $bindable(false),
    title,
    hint,
    onchange,
    disabled = false,
    class: cls = ''
  }: { checked?: boolean; title: string; hint?: string; onchange?: (checked: boolean) => void; disabled?: boolean; class?: string } = $props();
</script>

<label class="inline-flex cursor-pointer items-start gap-2.5 text-sm {disabled ? 'opacity-60' : ''} {cls}">
  <input
    type="checkbox"
    class="checkbox mt-0.5"
    bind:checked
    {disabled}
    onchange={(e) => onchange?.(e.currentTarget.checked)}
  />
  <span class="min-w-0">
    <span class="block text-fg">{title}</span>
    {#if hint}<span class="block text-xs text-fg-faint">{hint}</span>{/if}
  </span>
</label>
