<script lang="ts">
  let {
    checked = $bindable(false),
    title,
    description = '',
    onchange,
    class: cls = ''
  }: { checked?: boolean; title: string; description?: string; onchange?: (checked: boolean) => void; class?: string } = $props();
</script>

<label class="flex cursor-pointer items-start gap-3 rounded-lg border border-line bg-sunken px-3 py-2.5 {cls}">
  <input type="checkbox" class="mt-0.5 accent-accent" bind:checked onchange={(e) => onchange?.(e.currentTarget.checked)} />
  <span class="text-sm">
    <span class="font-medium text-fg">{title}</span>
    {#if description}<span class="block text-xs leading-5 text-fg-muted">{description}</span>{/if}
  </span>
</label>
