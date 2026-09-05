<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { page } from '$app/state';
  import { replaceState } from '$app/navigation';
  import { api, message } from '$lib/api';
  import { cached, refreshCached } from '$lib/state.svelte';
  import { fail } from '$lib/toast.svelte';
  import { groupByProvider, groupLabel, kindParam, looksLikeRepo, parseKind, pickGroup, sortReversible, sourceLabels } from '$lib/catalog';
  import { SourceKind, type SearchHit } from '$proto/source_pb';
  import { Search, Compass, LayoutGrid, List, ArrowDownWideNarrow, ArrowUpNarrowWide, Eye, KeyRound, X, RefreshCw, ExternalLink, Settings } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Skeleton from '$lib/components/ui/Skeleton.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import SourceTabs from '$lib/components/catalog/SourceTabs.svelte';
  import FacetPicker from '$lib/components/catalog/FacetPicker.svelte';
  import HitCard from '$lib/components/catalog/HitCard.svelte';
  import ModelDrawer from '$lib/components/ModelDrawer.svelte';

  const pageSize = 30;
  const viewKey = 'nebu.catalog.view';

  let kind = $state<SourceKind>(SourceKind.UNSPECIFIED);
  let sourceId = $state('');
  let query = $state('');
  let sort = $state('');
  let ascending = $state(false);
  let filters = $state<Record<string, string>>({});
  let hits = $state<SearchHit[]>([]);
  let nextCursor = $state('');
  let total = $state(0n);
  let warnings = $state<string[]>([]);
  let searching = $state(false);
  let loadingMore = $state(false);
  let searchError = $state('');
  let selected = $state<{ sourceId: string; repo: string; revision: string; hit: SearchHit | null } | null>(null);
  let drawerOpen = $state(false);
  let slotId = $state('');
  let view = $state<'grid' | 'list'>('grid');
  let input: HTMLInputElement | undefined = $state();
  let sentinel: HTMLDivElement | undefined = $state();
  let debounce: ReturnType<typeof setTimeout> | null = null;
  let generation = 0;
  let booted = false;

  const statuses = $derived(cached.sources);
  const groups = $derived(groupByProvider(statuses));
  // Every source at once, the tab before the providers
  const all = $derived(kind === SourceKind.UNSPECIFIED && sourceId === '' && groups.length > 0);
  const group = $derived(all ? undefined : pickGroup(groups, kind, sourceId));
  // One source answers for the provider's capabilities, the chosen one or the first that works
  const status = $derived(group?.sources.find((s) => s.source?.id === sourceId) ?? group?.sources.find((s) => !s.error) ?? group?.sources[0]);
  const caps = $derived(status?.capabilities);
  // Every hit and the drawer carry their own source's capabilities, which the All tab merges across
  const capsOf = (id: string) => statuses.find((s) => s.source?.id === id)?.capabilities ?? caps;
  const merged = $derived(all || (!!group && sourceId === '' && group.sources.length > 1));
  const labels = $derived(sourceLabels(all ? statuses : (group?.sources ?? [])));
  const name = $derived(all ? 'every source' : group ? (merged ? group.name : (labels.get(sourceId) ?? groupLabel(group))) : 'the source');
  const effectiveSort = $derived(sort || caps?.defaultSort || '');
  const sortLabel = $derived(caps?.sorts.find((s) => s.id === effectiveSort)?.label ?? effectiveSort);
  const reversible = $derived(sortReversible(caps, effectiveSort));
  const activeFilters = $derived(Object.entries(filters).filter(([, v]) => v));
  // The source whose repository form the typed text takes: the chosen one, else the first across every source
  const repoSource = $derived(all ? statuses.find((s) => !s.error && looksLikeRepo(s.capabilities, query)) : looksLikeRepo(caps, query) ? status : undefined);
  const canSubmitRepo = $derived(!!repoSource);
  // Sources of the provider that browse but cannot download without a token
  const tokenless = $derived((all ? statuses : merged ? (group?.sources ?? []) : status ? [status] : []).filter((s) => s.capabilities?.authRequired && !s.capabilities.tokenPresent));
  const browsing = $derived(!query.trim() && activeFilters.length === 0);
  // Listing needs a source that browses, matching one that searches, All merging whatever answers
  const searchable = $derived(all || (browsing ? !!caps?.browse : !!caps?.search));
  // Only a source that pages hands back a cursor, All when any of them does
  const paginates = $derived(all ? statuses.some((s) => s.capabilities?.paginate) : !!caps?.paginate);
  // With neither search nor a repository form the box has nothing to take
  const inputDead = $derived(!all && !!caps && !caps.search && !caps.repoPattern);
  // The source a typed repository opens in: the chosen one, else the provider's first working source
  const openSourceId = $derived(sourceId || status?.source?.id || statuses.find((s) => !s.error)?.source?.id || '');

  onMount(() => {
    try {
      const v = localStorage.getItem(viewKey);
      if (v === 'grid' || v === 'list') view = v;
    } catch {
      // storage may be unavailable
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === '/' && !e.metaKey && !e.ctrlKey && document.activeElement?.tagName !== 'INPUT' && document.activeElement?.tagName !== 'TEXTAREA' && document.activeElement?.tagName !== 'SELECT') {
        e.preventDefault();
        input?.focus();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });

  // The URL picks the source, query, sort, and filters once the cached sources are in
  $effect(() => {
    if (!cached.loaded || booted || statuses.length === 0) return;
    untrack(boot);
  });

  function boot() {
    const p = page.url.searchParams;
    const wantedSource = p.get('source') ?? '';
    const everything = p.get('provider') === 'all' && !wantedSource;
    const g = everything ? undefined : pickGroup(groups, parseKind(p.get('provider') ?? ''), wantedSource);
    kind = g?.kind ?? SourceKind.UNSPECIFIED;
    sourceId = g?.sources.some((s) => s.source?.id === wantedSource) ? wantedSource : g && g.sources.length === 1 ? (g.sources[0].source?.id ?? '') : '';
    query = p.get('q') ?? '';
    sort = p.get('sort') ?? '';
    ascending = p.get('asc') === '1';
    const f: Record<string, string> = {};
    for (const [k, v] of p) if (k.startsWith('f.') && v) f[k.slice(2)] = v;
    filters = f;
    const v = p.get('view');
    if (v === 'grid' || v === 'list') view = v;
    booted = true;
    search();
    const repo = p.get('repo');
    if (repo) openRepo(openSourceId, repo, p.get('rev') ?? '', null);
  }

  // A provider only accepts its own sorts and facets, so switching drops the rest, and searching everything carries none
  $effect(() => {
    if (all) {
      if (sort) sort = '';
      if (Object.keys(filters).length) filters = {};
      return;
    }
    if (!caps) return;
    if (sort && !caps.sorts.some((s) => s.id === sort)) sort = '';
    const keep: Record<string, string> = {};
    for (const [k, v] of Object.entries(filters)) if (caps.facets.some((f) => f.id === k)) keep[k] = v;
    if (Object.keys(keep).length !== Object.keys(filters).length) filters = keep;
  });

  // Only some sorts can be flipped, so the flag drops with the sort that allowed it
  $effect(() => {
    if (ascending && caps && !reversible) ascending = false;
  });

  // Everything that changes the result set restarts the search from page one
  $effect(() => {
    void kind;
    void sourceId;
    void effectiveSort;
    void ascending;
    void JSON.stringify(filters);
    if (booted) search();
  });

  function syncUrl() {
    const p = new URLSearchParams();
    if (kind) p.set('provider', kindParam(kind));
    else if (all) p.set('provider', 'all');
    if (sourceId) p.set('source', sourceId);
    if (query.trim()) p.set('q', query.trim());
    if (sort) p.set('sort', sort);
    if (ascending) p.set('asc', '1');
    for (const [k, v] of activeFilters) p.set('f.' + k, v);
    if (selected && drawerOpen) {
      p.set('repo', selected.repo);
      if (selected.revision) p.set('rev', selected.revision);
      if (selected.sourceId !== sourceId) p.set('source', selected.sourceId);
    }
    const qs = p.toString();
    const next = '/catalog' + (qs ? '?' + qs : '');
    if (next !== page.url.pathname + page.url.search) replaceState(next, {});
  }

  function request(cursor = '') {
    const f: Record<string, string> = {};
    for (const [k, v] of activeFilters) f[k] = v;
    return { sourceId, kind: sourceId ? SourceKind.UNSPECIFIED : kind, query: query.trim(), sort: effectiveSort, ascending: ascending && reversible, filters: f, limit: pageSize, cursor };
  }

  async function search() {
    if (!group && !all) return;
    syncUrl();
    if (!searchable) return;
    const gen = ++generation;
    searching = true;
    searchError = '';
    try {
      const r = await api.sources.search(request());
      if (gen !== generation) return;
      hits = r.hits;
      nextCursor = r.nextCursor;
      total = r.total;
      warnings = r.warnings;
    } catch (err) {
      if (gen !== generation) return;
      hits = [];
      nextCursor = '';
      total = 0n;
      warnings = [];
      searchError = message(err);
    } finally {
      if (gen === generation) searching = false;
    }
  }

  async function more() {
    if (!nextCursor || loadingMore || searching) return;
    const gen = generation;
    loadingMore = true;
    try {
      const r = await api.sources.search(request(nextCursor));
      if (gen !== generation) return;
      const seen = new Set(hits.map((h) => h.sourceId + '/' + h.repo));
      hits = [...hits, ...r.hits.filter((h) => !seen.has(h.sourceId + '/' + h.repo))];
      nextCursor = r.nextCursor;
      if (r.total) total = r.total;
      warnings = [...new Set([...warnings, ...r.warnings])];
    } catch (err) {
      fail(err, 'Could not load more');
    } finally {
      if (gen === generation) loadingMore = false;
    }
  }

  // Loads the next page as the last card scrolls into view
  $effect(() => {
    const el = sentinel;
    if (!el) return;
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) more();
    });
    io.observe(el);
    return () => io.disconnect();
  });

  function onInput() {
    if (debounce) clearTimeout(debounce);
    debounce = setTimeout(search, 350);
  }

  function submit(e: Event) {
    e.preventDefault();
    if (debounce) clearTimeout(debounce);
    if (repoSource && !hits.some((h) => h.repo === query.trim())) openRepo(repoSource.source?.id || openSourceId, query.trim(), '', null);
    search();
  }

  function pick(k: SourceKind, id: string) {
    if (k === kind && id === sourceId) return;
    kind = k;
    sourceId = id;
    input?.focus();
  }

  function openHit(h: SearchHit) {
    openRepo(h.sourceId || openSourceId, h.repo, '', h);
  }

  function openRepo(source: string, repo: string, revision: string, hit: SearchHit | null) {
    selected = { sourceId: source, repo, revision, hit: hit ?? hits.find((h) => h.repo === repo && (h.sourceId || openSourceId) === source) ?? null };
    drawerOpen = true;
    syncUrl();
  }

  $effect(() => {
    if (!drawerOpen && booted) syncUrl();
  });

  function setView(v: 'grid' | 'list') {
    view = v;
    try {
      localStorage.setItem(viewKey, v);
    } catch {
      // ignore
    }
  }

  function clearAll() {
    const wasClean = !query && !sort && !ascending && activeFilters.length === 0;
    query = '';
    filters = {};
    sort = '';
    ascending = false;
    if (wasClean) search();
  }
</script>

<PageHeader title="Catalog" description="Find a model, open it to see which of its weights fit this machine, then pull one. Nothing downloads until you say so.">
  {#snippet meta()}
    {#if caps?.webUrl}
      <a href={caps.webUrl} target="_blank" rel="noopener noreferrer" class="inline-flex items-center gap-1 hover:text-fg"><ExternalLink size={11} /> Open {name} in a new tab</a>
    {/if}
    <a href="/settings" class="inline-flex items-center gap-1 hover:text-fg"><Settings size={11} /> Manage sources</a>
  {/snippet}
  <div role="group" aria-label="Layout" class="inline-flex rounded-md border border-line bg-sunken p-0.5">
    <button class="rounded p-1.5 {view === 'grid' ? 'bg-raised text-fg' : 'text-fg-faint hover:text-fg'}" title="Cards" onclick={() => setView('grid')}><LayoutGrid size={14} /></button>
    <button class="rounded p-1.5 {view === 'list' ? 'bg-raised text-fg' : 'text-fg-faint hover:text-fg'}" title="List" onclick={() => setView('list')}><List size={14} /></button>
  </div>
</PageHeader>

{#if cached.error && statuses.length === 0}
  <div class="panel">
    <Empty icon={Compass} title="Sources unavailable" description={cached.error}>
      <Button size="sm" icon={RefreshCw} onclick={() => refreshCached()}>Try again</Button>
    </Empty>
  </div>
{:else if !cached.loaded}
  <div class="panel"><Skeleton rows={4} class="p-4" /></div>
{:else if statuses.length === 0}
  <div class="panel">
    <Empty icon={Compass} title="No sources" description="Add one in settings to browse a catalog.">
      <Button size="sm" icon={Settings} href="/settings">Open settings</Button>
    </Empty>
  </div>
{:else}
  <div class="mb-3">
    <SourceTabs {groups} {kind} {sourceId} onChange={pick} />
  </div>

  <form class="mb-3 flex flex-wrap items-center gap-2" onsubmit={submit}>
    <div class="relative min-w-64 flex-1">
      <Search size={15} class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-fg-faint" />
      <input
        bind:this={input}
        class="input pr-28 pl-9"
        bind:value={query}
        oninput={onInput}
        disabled={inputDead}
        placeholder={all ? 'Search every source at once' : inputDead ? `${name} can neither search nor open a repository by name` : caps?.search ? `Search ${name}, or paste ${caps?.repoExample || 'a repository name'} to open it` : `Type ${caps?.repoExample || 'a repository name'} to open it`}
        autocomplete="off"
        spellcheck="false"
      />
      <div class="pointer-events-none absolute top-1/2 right-2 flex -translate-y-1/2 items-center gap-1.5 text-[11px] text-fg-faint">
        {#if canSubmitRepo}<span class="pointer-events-auto rounded bg-accent/15 px-1.5 py-0.5 text-accent">Enter opens it</span>{:else if !query && !inputDead}<span class="kbd">/</span>{/if}
      </div>
    </div>
    {#if caps?.sorts.length}
      <label class="inline-flex items-center gap-1.5 text-xs text-fg-faint">
        <span class="hidden sm:inline">Sort</span>
        <select class="input w-44 shrink-0" bind:value={sort} aria-label="Sort by">
          {#each caps.sorts as s (s.id)}<option value={s.id === caps.defaultSort ? '' : s.id}>{s.label}</option>{/each}
        </select>
      </label>
      {#if reversible}
        <button
          type="button"
          class="inline-flex h-[2.125rem] items-center gap-1.5 rounded-md border border-line bg-raised px-2.5 text-xs text-fg-muted hover:text-fg"
          title={ascending ? `${sortLabel} ascending, click to descend` : `${sortLabel} descending, click to ascend`}
          onclick={() => (ascending = !ascending)}
        >
          {#if ascending}<ArrowUpNarrowWide size={14} /> Ascending{:else}<ArrowDownWideNarrow size={14} /> Descending{/if}
        </button>
      {/if}
    {/if}
    <Button type="submit" variant="primary" loading={searching} icon={canSubmitRepo ? Eye : Search} disabled={!canSubmitRepo && !searchable}>{canSubmitRepo ? 'Open' : 'Search'}</Button>
  </form>

  {#if caps?.facets.length}
    <div class="mb-4 flex flex-wrap items-center gap-2">
      <span class="text-xs text-fg-faint">Narrow by</span>
      {#each caps.facets as f (f.id)}
        <FacetPicker facet={f} value={filters[f.id] ?? ''} onChange={(v) => (filters = { ...filters, [f.id]: v })} />
      {/each}
      {#if activeFilters.length || query || sort}
        <button type="button" class="inline-flex h-8 items-center gap-1 rounded-md px-2 text-xs text-fg-faint hover:text-fg" onclick={clearAll}><X size={12} /> Clear</button>
      {/if}
    </div>
  {/if}

  {#each tokenless as s (s.source?.id)}
    <div class="mb-4 flex items-center gap-2 rounded-lg border border-warn/30 bg-warn/8 px-3 py-2 text-xs text-warn">
      <KeyRound size={13} class="shrink-0" />
      <span>{merged ? labels.get(s.source?.id ?? '') : name} lets you browse freely but needs a token to download. Set <span class="font-mono">{s.capabilities?.tokenEnv || 'its token variable'}</span> in the daemon's environment and restart it, or point the source at another variable in settings.</span>
    </div>
  {/each}

  {#if status?.error && !merged}
    <div class="mb-4 rounded-lg border border-bad/30 bg-bad/8 px-3 py-2 text-xs text-bad">{status.error}. <a href="/settings" class="underline">Fix its settings</a>.</div>
  {/if}

  <div class="mb-2 flex items-center gap-3 text-xs text-fg-faint">
    {#if searching}
      <span class="inline-flex items-center gap-1.5"><Spinner size={12} /> {browsing ? 'Loading' : 'Searching'} {name}…</span>
    {:else if hits.length}
      <span>
        {#if total > 0n}{hits.length.toLocaleString()} of {Number(total).toLocaleString()}{:else}{hits.length.toLocaleString()}{/if}
        {browsing ? 'models' : 'matches'} on {name}{#if sortLabel}, {sortLabel.toLowerCase()}{ascending && reversible ? ', ascending' : ''}{/if}
      </span>
    {/if}
  </div>

  {#each warnings as w (w)}
    <div class="mb-2 rounded-lg border border-warn/30 bg-warn/8 px-3 py-1.5 text-xs text-warn">{w}</div>
  {/each}

  {#if searchError}
    <div class="panel">
      <Empty compact icon={Compass} title="{name} did not answer" description={searchError}>
        <Button size="sm" icon={RefreshCw} onclick={search}>Try again</Button>
      </Empty>
    </div>
  {:else if !searching && hits.length === 0}
    <div class="panel">
      {#if !caps?.browse && browsing}
        <Empty icon={Eye} title="Type a repository to open it" description="{name} cannot list what it holds. Enter {caps?.repoExample || 'a repository name'} above to read its files and see what fits." />
      {:else if browsing}
        <Empty icon={Compass} title="Nothing listed" description="{name} answered with an empty catalog." />
      {:else if !searchable}
        <Empty icon={Eye} title="{name} does not search" description="Type an exact {caps?.repoExample || 'repository name'} to open it directly." />
      {:else}
        <Empty icon={Compass} title="No matches" description="Try fewer words, drop a filter, or type an exact {caps?.repoExample || 'repository name'} to open it directly.">
          <Button size="sm" variant="ghost" onclick={clearAll}>Clear filters</Button>
        </Empty>
      {/if}
    </div>
  {:else if searching && hits.length === 0}
    <div class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
      {#each [1, 2, 3, 4, 5, 6] as i (i)}<div class="panel p-4"><Skeleton rows={3} /></div>{/each}
    </div>
  {:else}
    <div class={view === 'grid' ? 'grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4' : 'flex flex-col gap-1.5'}>
      {#each hits as h (h.sourceId + '/' + h.repo)}
        <HitCard hit={h} caps={capsOf(h.sourceId)} runtimes={cached.runtimes} compact={view === 'list'} from={merged ? (labels.get(h.sourceId) ?? h.sourceId) : ''} selected={selected?.repo === h.repo && selected?.sourceId === h.sourceId && drawerOpen} onOpen={openHit} />
      {/each}
    </div>
    <div class="flex items-center justify-center py-6 text-xs text-fg-faint">
      {#if loadingMore}
        <span class="inline-flex items-center gap-1.5"><Spinner size={12} /> Loading more…</span>
      {:else if nextCursor && paginates}
        <div bind:this={sentinel}><Button size="sm" variant="outline" onclick={more}>Load more</Button></div>
      {:else if hits.length >= pageSize}
        <span>That is everything {name} listed.</span>
      {/if}
    </div>
  {/if}
{/if}

{#if selected}
  <ModelDrawer bind:open={drawerOpen} sourceId={selected.sourceId} sourceLabel={labels.get(selected.sourceId) ?? name} repo={selected.repo} revision={selected.revision} caps={capsOf(selected.sourceId)} runtimes={cached.runtimes} hit={selected.hit} bind:slotId onNavigate={(repo, rev) => { if (selected) selected = { ...selected, repo, revision: rev, hit: hits.find((h) => h.repo === repo) ?? null }; syncUrl(); }} />
{/if}
