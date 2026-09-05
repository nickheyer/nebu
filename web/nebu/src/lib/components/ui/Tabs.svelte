<script lang="ts">
  export interface Tab {
    id: string;
    label: string;
    count?: number;
    // A tab with a link navigates instead of setting the value
    href?: string;
  }

  // Underlined tabs, the active one carrying the accent
  let { tabs, value = $bindable(''), size = 'md', class: cls = '' }: { tabs: Tab[]; value?: string; size?: 'sm' | 'md'; class?: string } = $props();

  const item = $derived(
    `relative -mb-px inline-flex items-center gap-1.5 border-b-2 font-medium whitespace-nowrap transition-colors ${size === 'sm' ? 'h-8 px-2 text-xs' : 'h-9 px-2.5 text-sm'}`
  );
  const on = 'border-accent text-fg';
  const off = 'border-transparent text-fg-muted hover:text-fg';

  function key(e: KeyboardEvent, i: number) {
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
    e.preventDefault();
    const next = tabs[(i + (e.key === 'ArrowRight' ? 1 : tabs.length - 1)) % tabs.length];
    const el = (e.currentTarget as HTMLElement).parentElement?.children[tabs.indexOf(next)] as HTMLElement | undefined;
    el?.focus();
    if (!next.href) value = next.id;
  }
</script>

<div role="tablist" class="flex items-center gap-1 border-b border-line {cls}">
  {#each tabs as t, i (t.id)}
    {@const active = value === t.id}
    {#if t.href}
      <a href={t.href} role="tab" aria-selected={active} tabindex={active ? 0 : -1} class="{item} {active ? on : off}" onkeydown={(e) => key(e, i)}>
        {t.label}
        {#if t.count !== undefined}<span class="text-xs tabular-nums text-fg-faint">{t.count}</span>{/if}
      </a>
    {:else}
      <button type="button" role="tab" aria-selected={active} tabindex={active ? 0 : -1} class="{item} {active ? on : off}" onclick={() => (value = t.id)} onkeydown={(e) => key(e, i)}>
        {t.label}
        {#if t.count !== undefined}<span class="text-xs tabular-nums text-fg-faint">{t.count}</span>{/if}
      </button>
    {/if}
  {/each}
</div>
