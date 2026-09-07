<script lang="ts">
  import type { HTMLTextareaAttributes } from 'svelte/elements';

  // A multi line field that shows what applies while it is empty
  let {
    value = $bindable(''),
    empty = '',
    mono = false,
    invalid = false,
    height = 'h-28',
    class: cls = '',
    ...rest
  }: { value?: string; empty?: string; mono?: boolean; invalid?: boolean; height?: string; class?: string } & Omit<HTMLTextareaAttributes, 'value' | 'class'> = $props();
</script>

<div class="relative min-w-0 {cls}">
  <textarea class="input {height} {mono ? 'font-mono text-xs' : ''}" bind:value aria-invalid={invalid} spellcheck="false" {...rest}></textarea>
  {#if empty && !value}
    <span class="pointer-events-none absolute top-2 right-3 left-3 line-clamp-4 text-sm leading-6 text-fg-faint {mono ? 'font-mono text-xs' : ''}" aria-hidden="true">{empty}</span>
  {/if}
</div>
