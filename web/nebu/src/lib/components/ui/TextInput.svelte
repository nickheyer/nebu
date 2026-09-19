<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLInputAttributes } from 'svelte/elements';

  let {
    value = $bindable(''),
    empty = '',
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
    mono?: boolean;
    invalid?: boolean;
    size?: 'sm' | 'md';
    leading?: Snippet;
    inputClass?: string;
    class?: string;
  } & Omit<HTMLInputAttributes, 'value' | 'size' | 'class'> = $props();

  const indent = $derived(leading ? 'pl-9' : '');
</script>

<div class="relative min-w-0 {cls}">
  {#if leading}<span class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-fg-faint">{@render leading()}</span>{/if}
  <input class="input {size === 'sm' ? 'input-sm' : ''} {mono ? 'font-mono' : ''} {indent} {inputClass}" bind:value placeholder={empty} aria-invalid={invalid} autocomplete="off" spellcheck="false" {...rest} />
</div>
