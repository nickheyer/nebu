<script lang="ts">
  import type { Tone } from '$lib/format';

  let {
    tone = 'neutral',
    dot = false,
    pulse = false,
    size = 'sm',
    label,
    class: cls = ''
  }: { tone?: Tone; dot?: boolean; pulse?: boolean; size?: 'xs' | 'sm'; label?: string; class?: string } = $props();

  const tones: Record<Tone, string> = {
    ok: 'bg-ok/12 text-ok border-ok/25',
    warn: 'bg-warn/12 text-warn border-warn/25',
    bad: 'bg-bad/12 text-bad border-bad/25',
    info: 'bg-info/12 text-info border-info/25',
    accent: 'bg-accent/12 text-accent border-accent/25',
    neutral: 'bg-raised text-fg-muted border-line'
  };
</script>

<span
  class="inline-flex items-center gap-1.5 rounded-full border font-medium whitespace-nowrap {tones[tone]} {size === 'xs'
    ? 'h-[18px] px-1.5 text-[10.5px]'
    : 'h-[22px] px-2 text-[11.5px]'} {cls}"
>
  {#if dot}
    <span class="relative inline-block h-1.5 w-1.5 rounded-full bg-current {pulse ? 'pulse' : ''}"></span>
  {/if}
  {#if label}{label}{/if}
</span>
