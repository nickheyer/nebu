<script lang="ts">
  // An on or off setting that applies as soon as it is flipped
  let { checked = $bindable(false), title, hint, onchange, disabled = false }: { checked?: boolean; title: string; hint?: string; onchange?: (checked: boolean) => void; disabled?: boolean } = $props();

  function flip() {
    if (disabled) return;
    checked = !checked;
    onchange?.(checked);
  }
</script>

<div class="flex items-center gap-4">
  <div class="min-w-0 flex-1">
    <div class="text-sm text-fg">{title}</div>
    {#if hint}<div class="text-xs text-fg-faint">{hint}</div>{/if}
  </div>
  <button
    type="button"
    role="switch"
    aria-checked={checked}
    aria-label={title}
    {disabled}
    class="relative h-6 w-10 shrink-0 rounded-full border transition-colors {checked ? 'border-accent bg-accent' : 'border-line-strong bg-raised'} disabled:opacity-50"
    onclick={flip}
  >
    <span class="absolute top-0.5 h-[18px] w-[18px] rounded-full transition-[left] {checked ? 'left-[18px] bg-accent-fg' : 'left-0.5 bg-fg-muted'}"></span>
  </button>
</div>
