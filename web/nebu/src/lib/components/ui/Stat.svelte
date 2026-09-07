<script lang="ts">
  // A label over a value, for rows of figures
  let { label, value, sub, mono = false, tone = 'default' }: { label: string; value: string | number; sub?: string; mono?: boolean; tone?: 'default' | 'ok' | 'warn' | 'bad' | 'faint' } = $props();
  const colors = { default: 'text-fg', ok: 'text-ok', warn: 'text-warn', bad: 'text-bad', faint: 'text-fg-faint' };
</script>

<div class="flex min-w-0 flex-col gap-0.5">
  <div class="caps truncate text-fg-faint">{label}</div>
  <div class="truncate text-sm tabular-nums {mono ? 'font-mono' : ''} {colors[tone]}">{value}{#if sub}<span class="ml-1 text-xs text-fg-faint">{sub}</span>{/if}</div>
</div>
