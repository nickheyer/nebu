<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLInputAttributes } from 'svelte/elements';

  // A text field; a fallback applies while empty and is marked, an empty text is only a hint
  let {
    value = $bindable(''),
    empty = '',
    fallback = '',
    mono = false,
    invalid = false,
    size = 'md',
    leading,
    inputClass = '',
    class: cls = '',
    ...rest
  }: {
    value?: string;
    empty?: string;
    fallback?: string;
    mono?: boolean;
    invalid?: boolean;
    size?: 'sm' | 'md';
    // An icon drawn inside the field at its left edge
    leading?: Snippet;
    inputClass?: string;
    class?: string;
  } & Omit<HTMLInputAttributes, 'value' | 'size' | 'class'> = $props();

  const indent = $derived(leading ? 'pl-9' : '');
  const offset = $derived(leading ? 'left-9' : 'left-3');
  const blank = $derived(!value);
  const marked = $derived(blank && fallback !== '');
  const shown = $derived(blank ? fallback || empty : '');
</script>

<div class="relative min-w-0 {cls}">
  {#if leading}<span class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-fg-faint">{@render leading()}</span>{/if}
  <input class="input {size === 'sm' ? 'input-sm' : ''} {mono ? 'font-mono' : ''} {indent} {marked ? 'pr-18' : ''} {inputClass}" bind:value aria-invalid={invalid} autocomplete="off" spellcheck="false" {...rest} />
  {#if shown}
    <span class="pointer-events-none absolute inset-y-0 {offset} flex items-center text-sm text-fg-faint {mono ? 'font-mono' : ''}" aria-hidden="true">{shown}</span>
  {/if}
  {#if marked}
    <span class="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 rounded-sm border border-line px-1 text-[10px] font-medium tracking-wide text-fg-faint uppercase" aria-hidden="true">default</span>
  {/if}
</div>
