<script lang="ts">
  import type { Snippet } from 'svelte';

  // A labeled control: the label above, a faint line such as a flag name under it, a short description or
  // the error beneath the control
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
  </div>
  {@render children()}
  {#if error}<p class="text-xs text-bad">{error}</p>{:else if description}<p class="text-xs leading-5 text-fg-muted wrap-anywhere">{description}</p>{/if}
</div>
