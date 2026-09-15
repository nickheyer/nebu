<script lang="ts">
  // A number chosen on a slider between the bounds a runtime gives, the exact figure typed beside it;
  // the empty text shows what applies while nothing is chosen
  let {
    value = $bindable(''),
    id,
    min,
    max,
    step = 1,
    unit,
    empty = '',
    disabled = false,
    invalid = false,
    integer = false,
    class: cls = ''
  }: { value?: string; id?: string; min: number; max: number; step?: number; unit?: string; empty?: string; disabled?: boolean; invalid?: boolean; integer?: boolean; class?: string } = $props();

  const decimals = $derived(Math.max(0, (String(step).split('.')[1] ?? '').length));
  const blank = $derived(value.trim() === '');
  // The slider rests where the value or the empty text sits, at the floor when neither is a number
  const shown = $derived.by(() => {
    const v = parseFloat(blank ? empty : value);
    return Number.isFinite(v) ? Math.min(max, Math.max(min, v)) : min;
  });
  const fill = $derived(max > min ? ((shown - min) / (max - min)) * 100 : 0);

  function slide(e: Event) {
    const v = parseFloat((e.currentTarget as HTMLInputElement).value);
    value = integer ? String(Math.round(v)) : v.toFixed(decimals);
  }
</script>

<div class="flex min-w-0 items-center gap-3 {disabled ? 'opacity-50' : ''} {cls}">
  <input
    type="range"
    class="range min-w-0 flex-1"
    style="--fill: {fill}%"
    aria-label={id}
    {min}
    {max}
    {step}
    {disabled}
    value={shown}
    oninput={slide}
  />
  <div class="flex h-9 w-[7.5rem] shrink-0 items-center rounded-md border bg-sunken {invalid ? 'border-bad' : 'border-line focus-within:border-accent hover:border-line-strong'}">
    <input
      {id}
      type="number"
      class="input h-full! min-w-0 flex-1 border-0 bg-transparent px-2.5 font-mono focus-visible:outline-none"
      inputmode={integer ? 'numeric' : 'decimal'}
      {value}
      placeholder={empty}
      oninput={(e) => (value = e.currentTarget.value)}
      {min}
      {max}
      {step}
      {disabled}
      aria-invalid={invalid}
      autocomplete="off"
    />
    {#if unit}<span class="mr-2 text-xs text-fg-faint">{unit}</span>{/if}
  </div>
</div>
