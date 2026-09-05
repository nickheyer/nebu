<script lang="ts">
  import type { Tone } from '$lib/format';

  // A small tinted label, the one way state is shown everywhere
  let { tone = 'neutral', label, dot = false, pulse = false, class: cls = '' }: { tone?: Tone; label: string; dot?: boolean; pulse?: boolean; class?: string } = $props();

  const tones: Record<Tone, string> = {
    ok: 'bg-ok/12 text-ok',
    warn: 'bg-warn/12 text-warn',
    bad: 'bg-bad/12 text-bad',
    info: 'bg-info/12 text-info',
    accent: 'bg-accent/12 text-accent',
    neutral: 'bg-raised text-fg-muted'
  };
</script>

<span class="inline-flex h-6 shrink-0 items-center gap-1.5 rounded-full px-2.5 text-xs font-medium whitespace-nowrap {tones[tone]} {cls}">
  {#if dot}<span class="relative inline-block h-1.5 w-1.5 rounded-full bg-current {pulse ? 'pulse' : ''}"></span>{/if}
  {label}
</span>
