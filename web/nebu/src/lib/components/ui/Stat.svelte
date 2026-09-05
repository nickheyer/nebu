<script lang="ts">
  let { label, value, sub }: { label: string; value: string; sub?: string } = $props();
</script>

<div class="flex min-w-0 flex-col gap-0.5">
  <div class="text-[11px] font-semibold tracking-wider text-fg-faint uppercase">{label}</div>
  <div class="truncate text-lg font-semibold tabular-nums text-fg">{value}</div>
  {#if sub}<div class="truncate text-xs text-fg-muted">{sub}</div>{/if}
</div>
