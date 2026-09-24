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
  <div class="flex min-w-0 items-baseline justify-between gap-3">
    <label for={id} class="shrink-0 text-[13px] leading-5 font-medium text-fg">{label}{#if required}<span class="ml-0.5 text-bad" aria-hidden="true">*</span>{/if}</label>
    {#if error}<span class="min-w-0 truncate text-xs leading-5 text-bad" role="alert" title={error}>{error}</span>{/if}
  </div>
  {@render children()}
  {#if description}<p class="text-xs leading-5 text-fg-muted">{description}</p>{/if}
  {#if sub}<div class="min-w-0 truncate text-xs leading-5 text-fg-faint">{@render sub()}</div>{/if}
</div>
