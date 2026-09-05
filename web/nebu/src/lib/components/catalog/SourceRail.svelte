<script lang="ts">
  import { KeyRound, CircleAlert, Globe, ChevronDown } from '@lucide/svelte';
  import { SourceKind } from '$proto/source_pb';
  import { groupLabel, sourceLabels, type ProviderGroup } from '$lib/catalog';
  import Tip from '../ui/Tip.svelte';

  // Every provider in a list, its sources under it when it has several
  let {
    groups,
    kind,
    sourceId,
    onChange
  }: { groups: ProviderGroup[]; kind: SourceKind; sourceId: string; onChange: (kind: SourceKind, sourceId: string) => void } = $props();

  const labels = $derived(sourceLabels(groups.flatMap((g) => g.sources)));
  // Providers that can list come first, the ones that only open a typed name after
  const ordered = $derived([...groups].sort((a, b) => Number(lists(b)) - Number(lists(a))));

  function lists(g: ProviderGroup): boolean {
    return g.sources.some((s) => !s.error && (s.capabilities?.browse || s.capabilities?.search));
  }
  function needsToken(g: ProviderGroup): boolean {
    return g.sources.some((s) => s.capabilities?.authRequired && !s.capabilities.tokenPresent);
  }
  function broken(g: ProviderGroup): boolean {
    return g.sources.every((s) => !!s.error);
  }
  function hint(g: ProviderGroup): string {
    if (broken(g)) return g.sources[0]?.error || 'Not working';
    if (needsToken(g)) return `Downloads need ${g.sources[0]?.capabilities?.tokenEnv || 'a token'} set for the daemon`;
    if (!lists(g)) return 'Opens a typed repository only';
    return g.sources[0]?.capabilities?.description || g.name;
  }

  const item = 'flex h-8 w-full items-center gap-2 rounded-lg px-2.5 text-left text-sm transition-colors';
  const on = 'bg-raised font-medium text-fg';
  const off = 'text-fg-muted hover:bg-raised/60 hover:text-fg';
</script>

<nav aria-label="Sources" class="flex flex-col gap-0.5">
  {#if groups.length > 1}
    {@const all = kind === SourceKind.UNSPECIFIED && sourceId === ''}
    <button type="button" class="{item} {all ? on : off}" aria-current={all ? 'true' : undefined} onclick={() => onChange(SourceKind.UNSPECIFIED, '')}>
      <Globe size={15} class={all ? 'text-accent' : 'text-fg-faint'} />
      <span class="flex-1 truncate">All sources</span>
    </button>
    <div class="my-2 h-px bg-line"></div>
  {/if}
  {#each ordered as g (g.kind)}
    {@const active = g.kind === kind}
    {@const several = g.sources.length > 1}
    <Tip text={hint(g)} class="w-full">
      <button
        type="button"
        class="{item} {active && (!several || sourceId === '') ? on : off} {broken(g) ? 'opacity-60' : ''}"
        aria-current={active ? 'true' : undefined}
        onclick={() => onChange(g.kind, several ? '' : (g.sources[0].source?.id ?? ''))}
      >
        <span class="flex-1 truncate">{groupLabel(g)}</span>
        {#if broken(g)}
          <CircleAlert size={13} class="shrink-0 text-bad" />
        {:else if needsToken(g)}
          <KeyRound size={13} class="shrink-0 text-warn" />
        {/if}
        {#if several}<ChevronDown size={13} class="shrink-0 text-fg-faint transition-transform {active ? 'rotate-180' : ''}" />{/if}
      </button>
    </Tip>
    {#if active && several}
      <div class="mb-1 ml-3 flex flex-col gap-0.5 border-l border-line pl-2">
        {#each g.sources as s (s.source?.id)}
          {@const id = s.source?.id ?? ''}
          <button type="button" class="{item} h-7 text-xs {sourceId === id ? on : off}" title={s.error || s.capabilities?.endpoint || ''} onclick={() => onChange(kind, id)}>
            <span class="flex-1 truncate">{labels.get(id)}</span>
            {#if s.error}<CircleAlert size={12} class="text-bad" />{:else if s.capabilities?.authRequired && !s.capabilities.tokenPresent}<KeyRound size={12} class="text-warn" />{/if}
          </button>
        {/each}
      </div>
    {/if}
  {/each}
</nav>
