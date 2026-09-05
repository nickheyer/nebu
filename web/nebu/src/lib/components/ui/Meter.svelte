<script lang="ts">
  import { pct } from '$lib/format';
  import type { Tone } from '$lib/format';

  // A thin bar of value over max, warning as it nears full when asked to
  let {
    value,
    max,
    tone = 'accent',
    auto = false,
    height = 'md',
    class: cls = ''
  }: { value?: bigint | number; max: bigint | number; tone?: Tone; auto?: boolean; height?: 'sm' | 'md' | 'lg'; class?: string } = $props();

  const fills: Record<Tone, string> = { ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-bad', info: 'bg-info', accent: 'bg-accent', neutral: 'bg-fg-faint' };
  const heights = { sm: 'h-1', md: 'h-1.5', lg: 'h-2' };
  const p = $derived(pct(value, max));
  const fill = $derived<Tone>(auto ? (p >= 95 ? 'bad' : p >= 85 ? 'warn' : tone) : tone);
</script>

<div class="flex w-full overflow-hidden rounded-full bg-line/80 {heights[height]} {cls}">
  <div class="{fills[fill]} rounded-full transition-[width] duration-500" style="width: {p}%"></div>
</div>
