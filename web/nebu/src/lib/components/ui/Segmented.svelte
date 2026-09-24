<script lang="ts" module>
  export interface Segment {
    id: string;
    label: string;
    count?: number;
    unmet?: string;
  }
</script>

<script lang="ts">
  import Tip from './Tip.svelte';

  let { tabs, value = $bindable(''), size = 'md', class: cls = '' }: { tabs: Segment[]; value?: string; size?: 'sm' | 'md' | 'lg'; class?: string } = $props();
  // Outer heights match Button: sm 28px, md 32px, lg 36px (2px padding and a 1px border around each item).
  const items: Record<string, string> = { sm: 'h-[22px] px-2 text-xs', md: 'h-[26px] px-2.5 text-[13px]', lg: 'h-[30px] px-3 text-sm' };
  const item = $derived(`inline-flex items-center gap-1.5 rounded-[5px] font-medium whitespace-nowrap transition-colors ${items[size]}`);
</script>

{#snippet body(t: Segment, on: boolean)}
  {t.label}
  {#if t.count !== undefined}<span class="text-xs tabular-nums {on ? 'text-fg-muted' : 'text-fg-faint'}">{t.count}</span>{/if}
{/snippet}

<div role="radiogroup" class="inline-flex w-fit shrink-0 items-center gap-0.5 rounded-md border border-line bg-sunken p-0.5 {cls}">
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
