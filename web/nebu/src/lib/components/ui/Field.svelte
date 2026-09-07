<script lang="ts">
  import type { Snippet } from 'svelte';

  // A labeled control: the label above, a short description or the error beneath
  let {
    label,
    description,
    error,
    for: id,
    required = false,
    children,
    trailing,
    class: cls = ''
  }: { label: string; description?: string; error?: string; for?: string; required?: boolean; children: Snippet; trailing?: Snippet; class?: string } = $props();
</script>

<div class="flex min-w-0 flex-col gap-1.5 {cls}">
  <div class="flex items-center gap-1.5">
    <label for={id} class="text-[13px] font-medium text-fg">{label}{#if required}<span class="ml-0.5 text-bad" aria-hidden="true">*</span>{/if}</label>
    {#if trailing}<span class="ml-auto">{@render trailing()}</span>{/if}
  </div>
  {@render children()}
  {#if error}<p class="text-xs text-bad">{error}</p>{:else if description}<p class="text-xs leading-5 text-fg-muted">{description}</p>{/if}
</div>
