<script lang="ts">
  export interface Segment {
    id: string;
    label: string;
    count?: number;
  }

  // A small toggle between a few options, the chosen one raised
  let { tabs, value = $bindable(''), size = 'md', class: cls = '' }: { tabs: Segment[]; value?: string; size?: 'sm' | 'md'; class?: string } = $props();
  const item = $derived(`inline-flex items-center gap-1.5 rounded-[5px] font-medium whitespace-nowrap transition-colors ${size === 'sm' ? 'h-6 px-2 text-xs' : 'h-7 px-2.5 text-sm'}`);
</script>

<div role="radiogroup" class="inline-flex items-center gap-0.5 rounded-md border border-line bg-sunken p-0.5 {cls}">
  {#each tabs as t (t.id)}
    {@const on = value === t.id}
    <button type="button" role="radio" aria-checked={on} class="{item} {on ? 'bg-raised text-fg' : 'text-fg-muted hover:text-fg'}" onclick={() => (value = t.id)}>
      {t.label}
      {#if t.count !== undefined}<span class="text-xs tabular-nums {on ? 'text-fg-muted' : 'text-fg-faint'}">{t.count}</span>{/if}
    </button>
  {/each}
</div>
