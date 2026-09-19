<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    label,
    description,
    error,
    for: id,
    required = false,
    children,
    sub,
    class: cls = ''
  }: { label: string; description?: string; error?: string; for?: string; required?: boolean; children: Snippet; sub?: Snippet; class?: string } = $props();
</script>

<div class="flex min-w-0 flex-col gap-1.5 {cls}">
  <div class="flex min-w-0 flex-col">
    <label for={id} class="text-[13px] font-medium text-fg">{label}{#if required}<span class="ml-0.5 text-bad" aria-hidden="true">*</span>{/if}</label>
    {#if sub}<span class="min-w-0 truncate">{@render sub()}</span>{/if}
    {#if description}<p class="text-xs leading-5 text-fg-muted">{description}</p>{/if}
  </div>
  {@render children()}
  {#if error}<p class="text-xs text-bad">{error}</p>{/if}
</div>
