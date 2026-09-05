<script lang="ts" module>
  import type { Tone } from '$lib/format';

  export interface Segment {
    label: string;
    value: bigint | number;
    tone: Tone;
  }
</script>

<script lang="ts">
  import { bytes, pct } from '$lib/format';

  // Parts of one budget side by side, overflowing past the end in red when they do not fit
  let { segments, max, legend = true, height = 'md', class: cls = '' }: { segments: Segment[]; max: bigint | number; legend?: boolean; height?: 'sm' | 'md' | 'lg'; class?: string } = $props();

  const fills: Record<Tone, string> = { ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-bad', info: 'bg-info', accent: 'bg-accent', neutral: 'bg-fg-faint' };
  const heights = { sm: 'h-1', md: 'h-1.5', lg: 'h-2' };
  const total = $derived(segments.reduce((a, s) => a + Number(s.value), 0));
  const cap = $derived(Math.max(Number(max), 1));
  // Every segment scales to the larger of the budget and what is asked of it, so overflow stays visible
  const scale = $derived(Math.max(cap, total));
  const over = $derived(total > cap);
</script>

<div class="flex flex-col gap-1.5 {cls}">
  <div class="relative flex w-full overflow-hidden rounded-full bg-line/80 {heights[height]}">
    {#each segments as s (s.label)}
      {#if Number(s.value) > 0}
        <div class="{fills[s.tone]} transition-[width] duration-500 first:rounded-l-full" style="width: {(Number(s.value) / scale) * 100}%"></div>
      {/if}
    {/each}
    {#if over}
      <div class="absolute inset-y-0 w-px bg-bad" style="left: {pct(cap, scale)}%"></div>
    {/if}
  </div>
  {#if legend}
    <div class="flex flex-wrap gap-x-3 gap-y-0.5 text-xs tabular-nums text-fg-muted">
      {#each segments as s (s.label)}
        {#if Number(s.value) > 0}
          <span class="inline-flex items-center gap-1.5"><span class="inline-block h-1.5 w-1.5 rounded-full {fills[s.tone]}"></span>{s.label} <span class="text-fg">{bytes(s.value)}</span></span>
        {/if}
      {/each}
      <span class="ml-auto {over ? 'text-bad' : 'text-fg-faint'}">{bytes(total)} of {bytes(max)}</span>
    </div>
  {/if}
</div>
