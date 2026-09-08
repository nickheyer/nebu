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
  <textarea class="input {height} {mono ? 'font-mono text-xs' : ''}" bind:value placeholder={empty} aria-invalid={invalid} spellcheck="false" {...rest}></textarea>
</div>
