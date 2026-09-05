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
    ok: 'text-ok',
    warn: 'text-warn',
    bad: 'text-bad',
    info: 'text-info',
    accent: 'text-accent',
    neutral: 'text-fg-muted'
  };
</script>

<span class="inline-flex items-center gap-1.5 font-medium whitespace-nowrap {tones[tone]} {size === 'xs' ? 'text-[11px]' : 'text-xs'} {cls}">
  {#if dot}
    <span class="relative inline-block h-1.5 w-1.5 rounded-full bg-current {pulse ? 'pulse' : ''}"></span>
  {/if}
  {#if label}{label}{/if}
</span>
