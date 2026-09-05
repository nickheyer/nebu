<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Search, X } from '@lucide/svelte';
  import { slashFocus } from '$lib/keys';

  // A search field with the slash shortcut and a clear button once it holds text
  let {
    value = $bindable(''),
    placeholder = 'Search',
    size = 'md',
    disabled = false,
    oninput,
    onsubmit,
    trailing,
    element = $bindable(),
    class: cls = ''
  }: {
    value?: string;
    placeholder?: string;
    size?: 'md' | 'lg';
    disabled?: boolean;
    oninput?: () => void;
    onsubmit?: () => void;
    // Something drawn inside the field at its right edge, such as a hint
    trailing?: Snippet;
    element?: HTMLInputElement;
    class?: string;
  } = $props();

  const h = $derived(size === 'lg' ? 'h-9 pl-9 text-sm' : 'h-8 pl-8 text-sm');
</script>

<div class="relative {cls}">
  <Search size={size === 'lg' ? 15 : 14} class="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-fg-faint" />
  <input
    bind:this={element}
    use:slashFocus
    class="input {h} pr-16"
    bind:value
    {placeholder}
    {disabled}
    autocomplete="off"
    spellcheck="false"
    oninput={() => oninput?.()}
    onkeydown={(e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        onsubmit?.();
      } else if (e.key === 'Escape' && value) {
        value = '';
        oninput?.();
      }
    }}
  />
  <div class="absolute top-1/2 right-2 flex -translate-y-1/2 items-center gap-1">
    {#if trailing}{@render trailing()}{/if}
    {#if value}
      <button
        type="button"
        class="inline-flex h-5 w-5 items-center justify-center rounded-sm text-fg-faint hover:text-fg"
        aria-label="Clear"
        onclick={() => {
          value = '';
          oninput?.();
          element?.focus();
        }}><X size={13} /></button
      >
    {:else if !disabled}
      <span class="kbd pointer-events-none">/</span>
    {/if}
  </div>
</div>
