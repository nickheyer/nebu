<script lang="ts">
  let { checked = $bindable(false), label, onchange, disabled = false }: { checked?: boolean; label: string; onchange?: (checked: boolean) => void; disabled?: boolean } = $props();

  function flip() {
    if (disabled) return;
    checked = !checked;
    onchange?.(checked);
  }
</script>

<button
  type="button"
  role="switch"
  aria-checked={checked}
  aria-label={label}
  {disabled}
  class="relative h-5 w-9 shrink-0 rounded-full border transition-colors {checked ? 'border-accent bg-accent' : 'border-line-strong bg-raised'} disabled:opacity-50"
  onclick={flip}
>
  <span class="absolute top-0.5 h-3.5 w-3.5 rounded-full transition-[left] {checked ? 'left-[18px] bg-accent-fg' : 'left-0.5 bg-fg-muted'}"></span>
</button>
