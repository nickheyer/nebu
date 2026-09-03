<script lang="ts">
  export interface Tab {
    id: string;
    label: string;
    count?: number;
  }
  let { tabs, value = $bindable(''), size = 'md' }: { tabs: Tab[]; value?: string; size?: 'sm' | 'md' } = $props();
</script>

<div role="tablist" class="inline-flex items-center gap-0.5 rounded-lg border border-line bg-sunken p-0.5">
  {#each tabs as t (t.id)}
    <button
      role="tab"
      aria-selected={value === t.id}
      class="flex items-center gap-1.5 rounded-md font-medium transition-colors {size === 'sm' ? 'h-6 px-2 text-xs' : 'h-7 px-3 text-sm'} {value === t.id
        ? 'bg-raised text-fg shadow-sm'
        : 'text-fg-muted hover:text-fg'}"
      onclick={() => (value = t.id)}
    >
      {t.label}
      {#if t.count !== undefined}
        <span class="rounded-full px-1.5 text-[10.5px] tabular-nums {value === t.id ? 'bg-line text-fg' : 'bg-raised text-fg-faint'}">{t.count}</span>
      {/if}
    </button>
  {/each}
</div>
