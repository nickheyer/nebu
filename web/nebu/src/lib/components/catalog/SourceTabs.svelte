<script lang="ts">
  import { KeyRound, CircleAlert, Globe } from '@lucide/svelte';
  import { SourceKind } from '$proto/source_pb';
  import { groupLabel, sourceLabels, type ProviderGroup } from '$lib/catalog';

  let {
    groups,
    kind,
    sourceId,
    onChange
  }: { groups: ProviderGroup[]; kind: SourceKind; sourceId: string; onChange: (kind: SourceKind, sourceId: string) => void } = $props();

  const group = $derived(groups.find((g) => g.kind === kind));
  const labels = $derived(sourceLabels(group?.sources ?? []));

  function hint(g: ProviderGroup): string {
    const caps = g.sources[0]?.capabilities;
    const parts = [caps?.description || ''];
    if (g.sources.length > 1) parts.push(`${g.sources.length} sources merged`);
    if (g.sources.some((s) => s.capabilities?.authRequired && !s.capabilities.tokenPresent)) parts.push('Browsing is open, downloading needs a token');
    else if (!caps?.browse) parts.push('Cannot list what it holds, type a repository name');
    return parts.filter(Boolean).join('. ');
  }
</script>

<div class="flex flex-col gap-2">
  <div role="tablist" aria-label="Providers" class="flex flex-wrap items-center gap-1.5">
    {#if groups.length > 1}
      {@const on = kind === SourceKind.UNSPECIFIED && sourceId === ''}
      <button
        type="button"
        role="tab"
        aria-selected={on}
        title="Every source at once, each provider's own order interleaved. Sorts and facets belong to one provider, so they wait until one is picked"
        class="inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-xs font-medium transition-colors {on
          ? 'border-accent/50 bg-accent/12 text-fg'
          : 'border-line bg-surface text-fg-muted hover:border-line-strong hover:bg-raised/40 hover:text-fg'}"
        onclick={() => onChange(SourceKind.UNSPECIFIED, '')}
      >
        <Globe size={12} /> All
      </button>
    {/if}
    {#each groups as g (g.kind)}
      {@const on = g.kind === kind}
      {@const needsToken = g.sources.some((s) => s.capabilities?.authRequired && !s.capabilities.tokenPresent)}
      {@const broken = g.sources.every((s) => !!s.error)}
      <button
        type="button"
        role="tab"
        aria-selected={on}
        title={hint(g)}
        class="inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-xs font-medium transition-colors {on
          ? 'border-accent/50 bg-accent/12 text-fg'
          : 'border-line bg-surface text-fg-muted hover:border-line-strong hover:bg-raised/40 hover:text-fg'}"
        onclick={() => onChange(g.kind, g.sources.length === 1 ? (g.sources[0].source?.id ?? '') : '')}
      >
        {groupLabel(g)}
        {#if g.sources.length > 1}<span class="rounded-full bg-line px-1.5 text-[10px] tabular-nums text-fg-muted">{g.sources.length}</span>{/if}
        {#if broken}<CircleAlert size={11} class="text-bad" aria-label="not working" />{:else if needsToken}<KeyRound size={11} class="text-warn" aria-label="token needed to download" />{/if}
      </button>
    {/each}
  </div>
  {#if group && group.sources.length > 1}
    <div role="tablist" aria-label="Sources of {group.name}" class="flex flex-wrap items-center gap-1.5 text-xs">
      <span class="text-fg-faint">Source</span>
      <button type="button" role="tab" aria-selected={sourceId === ''} class="rounded-md border px-2 py-0.5 transition-colors {sourceId === '' ? 'border-accent/50 bg-accent/12 text-fg' : 'border-line text-fg-muted hover:text-fg'}" onclick={() => onChange(kind, '')}>All {group.sources.length}</button>
      {#each group.sources as s (s.source?.id)}
        {@const id = s.source?.id ?? ''}
        <button
          type="button"
          role="tab"
          aria-selected={sourceId === id}
          title={s.error || s.capabilities?.endpoint || s.capabilities?.webUrl || ''}
          class="inline-flex items-center gap-1 rounded-md border px-2 py-0.5 transition-colors {sourceId === id ? 'border-accent/50 bg-accent/12 text-fg' : 'border-line text-fg-muted hover:text-fg'}"
          onclick={() => onChange(kind, id)}
        >
          {labels.get(id)}
          {#if s.error}<CircleAlert size={11} class="text-bad" />{:else if s.capabilities?.authRequired && !s.capabilities.tokenPresent}<KeyRound size={11} class="text-warn" />{/if}
        </button>
      {/each}
    </div>
  {/if}
</div>
