<script lang="ts">
  import { pct } from '$lib/format';
  import type { Tone } from '$lib/format';

  let {
    value,
    max,
    tone,
    height = 'md',
    class: cls = ''
  }: { value?: bigint | number; max: bigint | number; tone?: Tone; height?: 'sm' | 'md' | 'lg'; class?: string } = $props();

  const fills: Record<Tone, string> = {
    ok: 'bg-ok',
    warn: 'bg-warn',
    bad: 'bg-bad',
    info: 'bg-info',
    accent: 'bg-accent',
    neutral: 'bg-fg-faint'
  };
  const heights = { sm: 'h-1', md: 'h-1.5', lg: 'h-2' };
  const auto = $derived.by((): Tone => {
    if (tone) return tone;
    const p = pct(value, max);
    if (p >= 95) return 'bad';
    if (p >= 80) return 'warn';
    return 'accent';
  });
</script>

<div class="flex w-full overflow-hidden rounded-full bg-line/70 {heights[height]} {cls}">
  <div class="{fills[auto]} rounded-full transition-[width] duration-500" style="width: {pct(value, max)}%"></div>
</div>
