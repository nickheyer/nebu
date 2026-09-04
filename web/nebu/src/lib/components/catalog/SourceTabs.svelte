<script lang="ts">
  import { KeyRound } from '@lucide/svelte';
  import type { SourceStatus } from '$proto/source_pb';
  import { sourceLabels } from '$lib/catalog';

  let { statuses, value, onChange }: { statuses: SourceStatus[]; value: string; onChange: (id: string) => void } = $props();

  const labels = $derived(sourceLabels(statuses));

  function hint(s: SourceStatus): string {
    const caps = s.capabilities;
    const parts = [caps?.description || ''];
    if (caps?.authRequired && !caps.tokenPresent) parts.push('Browsing is open, downloading needs a token');
    else if (!caps?.browse) parts.push('Cannot list what it holds, type a repository name');
    return parts.filter(Boolean).join('. ');
  }
</script>

<div role="tablist" aria-label="Catalog" class="flex flex-wrap items-center gap-1.5">
  {#each statuses as s (s.source?.id)}
    {@const id = s.source?.id ?? ''}
    {@const on = id === value}
    {@const needsToken = !!s.capabilities?.authRequired && !s.capabilities?.tokenPresent}
    <button
      type="button"
      role="tab"
      aria-selected={on}
      title={hint(s)}
      class="inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-xs font-medium transition-colors {on
        ? 'border-accent/50 bg-accent/12 text-fg'
        : 'border-line bg-surface text-fg-muted hover:border-line-strong hover:bg-raised/40 hover:text-fg'}"
      onclick={() => onChange(id)}
    >
      {labels.get(id)}
      {#if needsToken}<KeyRound size={11} class="text-warn" aria-label="token needed to download" />{/if}
    </button>
  {/each}
</div>
