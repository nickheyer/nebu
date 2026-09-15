<script lang="ts" module>
  export interface Segment {
    id: string;
    label: string;
    count?: number;
    // Why this host cannot take the option, which disables it; empty when it can
    unmet?: string;
  }
</script>

<script lang="ts">
  import Tip from './Tip.svelte';

  // A small toggle between a few options, the chosen one raised; lg stands as tall as a text field so
  // the two line up in a form
  let { tabs, value = $bindable(''), size = 'md', class: cls = '' }: { tabs: Segment[]; value?: string; size?: 'sm' | 'md' | 'lg'; class?: string } = $props();
  const boxes: Record<string, string> = { sm: 'p-0.5', md: 'p-0.5', lg: 'p-[3px]' };
  const items: Record<string, string> = { sm: 'h-6 px-2 text-xs', md: 'h-7 px-2.5 text-sm', lg: 'h-7 px-3 text-sm' };
  const item = $derived(`inline-flex items-center gap-1.5 rounded-[5px] font-medium whitespace-nowrap transition-colors ${items[size]}`);
</script>

{#snippet body(t: Segment, on: boolean)}
  {t.label}
  {#if t.count !== undefined}<span class="text-xs tabular-nums {on ? 'text-fg-muted' : 'text-fg-faint'}">{t.count}</span>{/if}
{/snippet}

<div role="radiogroup" class="inline-flex items-center gap-0.5 rounded-md border border-line bg-sunken {boxes[size]} {cls}">
  {#each tabs as t (t.id)}
    {@const on = value === t.id}
    {#if t.unmet}
      <Tip text={t.unmet}>
        <button type="button" role="radio" aria-checked={on} disabled class="{item} pointer-events-none text-fg-faint">{@render body(t, on)}</button>
      </Tip>
    {:else}
      <button type="button" role="radio" aria-checked={on} class="{item} {on ? 'bg-raised text-fg' : 'text-fg-muted hover:text-fg'}" onclick={() => (value = t.id)}>{@render body(t, on)}</button>
    {/if}
  {/each}
</div>
