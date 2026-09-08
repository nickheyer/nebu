<script lang="ts">
  import { Minus, Plus } from '@lucide/svelte';

  // A number field with a unit and step buttons; the empty text shows what applies while nothing is typed
  let {
    value = $bindable(''),
    id,
    min,
    max,
    step,
    unit,
    empty = '',
    disabled = false,
    invalid = false,
    integer = false,
    size = 'md',
    class: cls = ''
  }: {
    value?: string;
    id?: string;
    min?: number;
    max?: number;
    step?: number;
    unit?: string;
    empty?: string;
    disabled?: boolean;
    invalid?: boolean;
    integer?: boolean;
    size?: 'sm' | 'md';
    class?: string;
  } = $props();

  const stepBy = $derived(step || (integer ? 1 : 0.1));
  const decimals = $derived(Math.max(0, (String(stepBy).split('.')[1] ?? '').length));
  const blank = $derived(value.trim() === '');

  // Stepping starts from the typed number, else from the empty text when that is a number, else from the floor
  function nudge(sign: number) {
    const base = parseFloat(blank ? empty : value);
    let next = (Number.isFinite(base) ? base : min ?? 0) + sign * stepBy;
    if (min !== undefined) next = Math.max(min, next);
    if (max !== undefined && max !== 0) next = Math.min(max, next);
    value = integer ? String(Math.round(next)) : next.toFixed(decimals);
  }
</script>

<div class="flex min-w-0 items-center rounded-md border bg-sunken transition-colors {invalid ? 'border-bad focus-within:border-bad' : 'border-line hover:border-line-strong focus-within:border-accent'} {disabled ? 'cursor-not-allowed opacity-50' : ''} {size === 'sm' ? 'h-8' : 'h-9'} {cls}">
  <input
    {id}
    type="number"
    class="input h-full! min-w-0 flex-1 border-0 bg-transparent px-3 font-mono focus-visible:outline-none"
    inputmode={integer ? 'numeric' : 'decimal'}
    {value}
    placeholder={empty}
    oninput={(e) => (value = e.currentTarget.value)}
    {min}
    max={max || undefined}
    step={stepBy}
    {disabled}
    aria-invalid={invalid}
    autocomplete="off"
  />
  <div class="flex shrink-0 items-center gap-0.5 pr-1">
    {#if unit}<span class="mr-1 text-xs text-fg-faint">{unit}</span>{/if}
    <button type="button" tabindex="-1" class="flex h-6 w-6 items-center justify-center rounded-sm text-fg-faint hover:bg-raised hover:text-fg disabled:opacity-40" aria-label="Decrease" {disabled} onclick={() => nudge(-1)}><Minus size={12} /></button>
    <button type="button" tabindex="-1" class="flex h-6 w-6 items-center justify-center rounded-sm text-fg-faint hover:bg-raised hover:text-fg disabled:opacity-40" aria-label="Increase" {disabled} onclick={() => nudge(1)}><Plus size={12} /></button>
  </div>
</div>
