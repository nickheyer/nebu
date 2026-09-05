<script lang="ts">
  export interface Segment {
    id: string;
    label: string;
    count?: number;
    // A segment with a link navigates instead of setting the value
    href?: string;
  }
  let { tabs, value = $bindable(''), size = 'md', class: cls = '' }: { tabs: Segment[]; value?: string; size?: 'sm' | 'md'; class?: string } = $props();
  const item = $derived(`flex items-center gap-1.5 rounded-md font-medium transition-colors ${size === 'sm' ? 'h-7 px-2.5 text-xs' : 'h-8 px-3 text-sm'}`);
</script>

<div role="tablist" class="inline-flex items-center gap-0.5 rounded-lg border border-line bg-sunken p-0.5 {cls}">
  {#each tabs as t (t.id)}
    {@const on = value === t.id}
    {#if t.href}
      <a href={t.href} role="tab" aria-selected={on} class="{item} {on ? 'bg-raised text-fg shadow-sm' : 'text-fg-muted hover:text-fg'}">
        {t.label}
        {#if t.count !== undefined}<span class="text-xs tabular-nums {on ? 'text-fg-muted' : 'text-fg-faint'}">{t.count}</span>{/if}
      </a>
    {:else}
      <button type="button" role="tab" aria-selected={on} class="{item} {on ? 'bg-raised text-fg shadow-sm' : 'text-fg-muted hover:text-fg'}" onclick={() => (value = t.id)}>
        {t.label}
        {#if t.count !== undefined}<span class="text-xs tabular-nums {on ? 'text-fg-muted' : 'text-fg-faint'}">{t.count}</span>{/if}
      </button>
    {/if}
  {/each}
</div>
