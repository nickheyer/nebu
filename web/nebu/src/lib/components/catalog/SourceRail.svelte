<script lang="ts">
  import { KeyRound, CircleAlert, Globe, ChevronDown, Plus, Settings2 } from '@lucide/svelte';
  import { SourceKind, type SourceStatus } from '$proto/source_pb';
  import { groupLabel, sourceLabels, type ProviderGroup } from '$lib/catalog';
  import Tip from '../ui/Tip.svelte';

  // Every provider in a list, its sources under it when it has several, each source edited from here and new ones added at the foot
  let {
    groups,
    kind,
    sourceId,
    onChange,
    onEdit,
    onAdd
  }: { groups: ProviderGroup[]; kind: SourceKind; sourceId: string; onChange: (kind: SourceKind, sourceId: string) => void; onEdit: (s: SourceStatus) => void; onAdd: () => void } = $props();

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
    if (broken(g)) return g.sources[0]?.error || 'Not answering';
    if (needsToken(g)) return `Set ${g.sources[0]?.capabilities?.tokenEnv || 'a token'} to download`;
    if (!lists(g)) return 'No browsing. Type a repository name to open it.';
    return g.sources[0]?.capabilities?.description || g.name;
  }

  const item = 'relative flex h-8 min-w-0 flex-1 items-center gap-2 rounded-md px-2.5 text-left text-sm transition-colors';
  const on = 'bg-raised/70 font-medium text-fg';
  const off = 'text-fg-muted hover:bg-raised/50 hover:text-fg';
  const gear = 'flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-fg-faint/60 transition-colors hover:bg-raised hover:text-fg';
</script>

<nav aria-label="Sources" class="flex flex-col gap-0.5">
  {#if groups.length > 1}
    {@const all = kind === SourceKind.UNSPECIFIED && sourceId === ''}
    <button type="button" class="{item} {all ? on : off}" aria-current={all ? 'true' : undefined} onclick={() => onChange(SourceKind.UNSPECIFIED, '')}>
      {#if all}<span class="absolute inset-y-1.5 left-0 w-0.5 rounded-full bg-accent"></span>{/if}
      <Globe size={14} class={all ? 'text-accent' : 'text-fg-faint'} />
      <span class="flex-1 truncate">All sources</span>
    </button>
    <div class="my-1.5 h-px bg-line"></div>
  {/if}
  {#each ordered as g (g.kind)}
    {@const active = g.kind === kind}
    {@const several = g.sources.length > 1}
    {@const lit = active && (!several || sourceId === '')}
    <div class="group/row flex items-center gap-0.5">
      <Tip text={hint(g)} side="right" class="min-w-0 flex-1">
        <button
          type="button"
          class="{item} {lit ? on : off} {broken(g) ? 'opacity-50' : ''}"
          aria-current={active ? 'true' : undefined}
          onclick={() => onChange(g.kind, several ? '' : (g.sources[0].source?.id ?? ''))}
        >
          {#if lit}<span class="absolute inset-y-1.5 left-0 w-0.5 rounded-full bg-accent"></span>{/if}
          <span class="flex-1 truncate">{groupLabel(g)}</span>
          {#if broken(g)}
            <CircleAlert size={12} class="shrink-0 text-bad" />
          {:else if needsToken(g)}
            <KeyRound size={12} class="shrink-0 text-warn" />
          {/if}
          {#if several}<ChevronDown size={12} class="shrink-0 text-fg-faint transition-transform {active ? 'rotate-180' : ''}" />{/if}
        </button>
      </Tip>
      {#if !several}
        <button type="button" class={gear} aria-label="Settings of {groupLabel(g)}" onclick={() => onEdit(g.sources[0])}><Settings2 size={13} /></button>
      {/if}
    </div>
    {#if active && several}
      <div class="mb-1 ml-3 flex flex-col gap-0.5 border-l border-line pl-2">
        {#each g.sources as s (s.source?.id)}
          {@const id = s.source?.id ?? ''}
          <div class="flex items-center gap-0.5">
            <button type="button" class="{item} h-7 text-xs {sourceId === id ? on : off}" title={s.error || s.capabilities?.endpoint || ''} onclick={() => onChange(kind, id)}>
              <span class="flex-1 truncate">{labels.get(id)}</span>
              {#if s.error}<CircleAlert size={11} class="text-bad" />{:else if s.capabilities?.authRequired && !s.capabilities.tokenPresent}<KeyRound size={11} class="text-warn" />{/if}
            </button>
            <button type="button" class={gear} aria-label="Settings of {labels.get(id)}" onclick={() => onEdit(s)}><Settings2 size={13} /></button>
          </div>
        {/each}
      </div>
    {/if}
  {/each}
  <div class="my-1.5 h-px bg-line"></div>
  <button type="button" class="{item} {off}" onclick={onAdd}>
    <Plus size={14} class="text-fg-faint" />
    <span class="flex-1 truncate">Add source</span>
  </button>
</nav>
