<script lang="ts">
  import Info from './Info.svelte';

  let {
    checked = $bindable(false),
    label,
    info,
    onchange,
    disabled = false,
    class: cls = ''
  }: { checked?: boolean; label: string; info?: string; onchange?: (checked: boolean) => void; disabled?: boolean; class?: string } = $props();
</script>

<label class="inline-flex h-8 cursor-pointer items-center gap-2.5 text-sm text-fg {disabled ? 'opacity-50' : ''} {cls}">
  <input type="checkbox" class="checkbox" bind:checked {disabled} onchange={(e) => onchange?.(e.currentTarget.checked)} />
  <span>{label}</span>
  {#if info}<Info text={info} />{/if}
</label>
