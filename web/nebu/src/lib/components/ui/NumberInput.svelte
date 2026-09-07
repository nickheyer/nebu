<script lang="ts">
  import { Minus, Plus } from '@lucide/svelte';

  // A number field with a unit and step buttons, showing what applies while it is empty
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

  function nudge(sign: number) {
    const base = value.trim() === '' ? parseFloat(empty) : parseFloat(value);
    let next = (Number.isFinite(base) ? base : min ?? 0) + sign * stepBy;
    if (min !== undefined) next = Math.max(min, next);
    if (max !== undefined && max !== 0) next = Math.min(max, next);
    value = integer ? String(Math.round(next)) : next.toFixed(decimals);
  }
</script>

<div class="relative flex min-w-0 items-center {cls}">
  <input
    {id}
    type="number"
    class="input {size === 'sm' ? 'input-sm' : ''} font-mono"
    style="padding-right: {unit ? 4.25 + unit.length * 0.5 : 3.75}rem"
    inputmode={integer ? 'numeric' : 'decimal'}
    bind:value
    {min}
    max={max || undefined}
    step={stepBy}
    {disabled}
    aria-invalid={invalid}
    autocomplete="off"
  />
  {#if empty && value === ''}
    <span class="pointer-events-none absolute inset-y-0 left-3 flex items-center font-mono text-sm text-fg-faint" aria-hidden="true">{empty}</span>
  {/if}
  <div class="pointer-events-none absolute inset-y-0 right-0 flex items-center gap-0.5 pr-1">
    {#if unit}<span class="mr-1 text-xs text-fg-faint">{unit}</span>{/if}
    <button type="button" tabindex="-1" class="pointer-events-auto flex h-6 w-6 items-center justify-center rounded-sm text-fg-faint hover:bg-raised hover:text-fg disabled:opacity-40" aria-label="Decrease" {disabled} onclick={() => nudge(-1)}><Minus size={12} /></button>
    <button type="button" tabindex="-1" class="pointer-events-auto flex h-6 w-6 items-center justify-center rounded-sm text-fg-faint hover:bg-raised hover:text-fg disabled:opacity-40" aria-label="Increase" {disabled} onclick={() => nudge(1)}><Plus size={12} /></button>
  </div>
</div>
