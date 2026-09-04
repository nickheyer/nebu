<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { replaceState } from '$app/navigation';
  import { api, message } from '$lib/api';
  import { fail } from '$lib/toast.svelte';
  import { looksLikeRepo, pickSource, sortReversible, sourceName } from '$lib/catalog';
  import type { SearchHit, SourceStatus } from '$proto/source_pb';
  import type { RuntimeStatus } from '$proto/runtime_pb';
  import { Search, Compass, LayoutGrid, List, ArrowDownWideNarrow, ArrowUpNarrowWide, Eye, KeyRound, X, RefreshCw, ExternalLink } from '@lucide/svelte';
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

  let statuses = $state<SourceStatus[]>([]);
  let runtimes = $state<RuntimeStatus[]>([]);
  let sourcesError = $state('');
  let sourceId = $state('');
  let query = $state('');
  let sort = $state('');
  let ascending = $state(false);
  let filters = $state<Record<string, string>>({});
  let hits = $state<SearchHit[]>([]);
  let nextCursor = $state('');
  let total = $state(0n);
  let searching = $state(false);
  let loadingMore = $state(false);
  let searchError = $state('');
  let selected = $state<{ repo: string; revision: string; hit: SearchHit | null } | null>(null);
  let drawerOpen = $state(false);
  let slotId = $state('');
  let view = $state<'grid' | 'list'>('grid');
  let input: HTMLInputElement | undefined = $state();
  let sentinel: HTMLDivElement | undefined = $state();
  let debounce: ReturnType<typeof setTimeout> | null = null;
  let generation = 0;
  let booted = false;

  const status = $derived(pickSource(statuses, sourceId));
  const source = $derived(status?.source);
  const caps = $derived(status?.capabilities);
  const name = $derived(status ? sourceName(status) : 'the source');
  const effectiveSort = $derived(sort || caps?.defaultSort || '');
  const sortLabel = $derived(caps?.sorts.find((s) => s.id === effectiveSort)?.label ?? effectiveSort);
  const reversible = $derived(sortReversible(caps, effectiveSort));
  // Words for each direction that match what the sort orders by
  const direction = $derived.by(() => {
    if (effectiveSort === 'name') return { desc: 'Z to A', asc: 'A to Z' };
    if (effectiveSort === 'updated' || effectiveSort === 'created') return { desc: 'Newest first', asc: 'Oldest first' };
    return { desc: 'Highest first', asc: 'Lowest first' };
  });
  const activeFilters = $derived(Object.entries(filters).filter(([, v]) => v));
  const canSubmitRepo = $derived(looksLikeRepo(caps, query));
  const needsToken = $derived(!!caps?.authRequired && !caps?.tokenPresent);
  const browsing = $derived(!query.trim() && activeFilters.length === 0);

  onMount(() => {
    try {
      const v = localStorage.getItem(viewKey);
      if (v === 'grid' || v === 'list') view = v;
    } catch {
      // storage may be unavailable
    }
    loadSources();
    api.runtimes
      .listRuntimes({})
      .then((r) => (runtimes = r.runtimes))
      .catch(() => (runtimes = []));
    const onKey = (e: KeyboardEvent) => {
      if (e.key === '/' && !e.metaKey && !e.ctrlKey && document.activeElement?.tagName !== 'INPUT' && document.activeElement?.tagName !== 'TEXTAREA' && document.activeElement?.tagName !== 'SELECT') {
        e.preventDefault();
        input?.focus();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });

  async function loadSources() {
    try {
      statuses = (await api.sources.listSources({})).sources;
      sourcesError = '';
    } catch (err) {
      sourcesError = message(err);
      fail(err, 'Could not list sources');
      return;
    }
    const p = page.url.searchParams;
    const wanted = p.get('source') ?? '';
    sourceId = pickSource(statuses, wanted)?.source?.id ?? '';
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
    if (repo) openRepo(repo, p.get('rev') ?? '', null);
  }

  // A source only accepts its own sorts and facets, so switching drops the rest
  $effect(() => {
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
    void sourceId;
    void effectiveSort;
    void ascending;
    void JSON.stringify(filters);
    if (booted) search();
  });

  function syncUrl() {
    const p = new URLSearchParams();
    if (sourceId) p.set('source', sourceId);
    if (query.trim()) p.set('q', query.trim());
    if (sort) p.set('sort', sort);
    if (ascending) p.set('asc', '1');
    for (const [k, v] of activeFilters) p.set('f.' + k, v);
    if (selected && drawerOpen) {
      p.set('repo', selected.repo);
      if (selected.revision) p.set('rev', selected.revision);
    }
    const qs = p.toString();
    const next = '/catalog' + (qs ? '?' + qs : '');
    if (next !== page.url.pathname + page.url.search) replaceState(next, {});
  }

  function request(cursor = '') {
    const f: Record<string, string> = {};
    for (const [k, v] of activeFilters) f[k] = v;
    return { sourceId, query: query.trim(), sort: effectiveSort, ascending: ascending && reversible, filters: f, limit: pageSize, cursor };
  }

  async function search() {
    if (!sourceId) return;
    const gen = ++generation;
    searching = true;
    searchError = '';
    syncUrl();
    try {
      const r = await api.sources.search(request());
      if (gen !== generation) return;
      hits = r.hits;
      nextCursor = r.nextCursor;
      total = r.total;
    } catch (err) {
      if (gen !== generation) return;
      hits = [];
      nextCursor = '';
      total = 0n;
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
      const seen = new Set(hits.map((h) => h.repo));
      hits = [...hits, ...r.hits.filter((h) => !seen.has(h.repo))];
      nextCursor = r.nextCursor;
      if (r.total) total = r.total;
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
    if (canSubmitRepo && !hits.some((h) => h.repo === query.trim())) openRepo(query.trim(), '', null);
    search();
  }

  function pickSourceId(id: string) {
    if (id === sourceId) return;
    sourceId = id;
    input?.focus();
  }

  function openHit(h: SearchHit) {
    openRepo(h.repo, '', h);
  }

  function openRepo(repo: string, revision: string, hit: SearchHit | null) {
    selected = { repo, revision, hit: hit ?? hits.find((h) => h.repo === repo) ?? null };
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
  {/snippet}
  <div role="group" aria-label="Layout" class="inline-flex rounded-md border border-line bg-sunken p-0.5">
    <button class="rounded p-1.5 {view === 'grid' ? 'bg-raised text-fg' : 'text-fg-faint hover:text-fg'}" title="Cards" onclick={() => setView('grid')}><LayoutGrid size={14} /></button>
    <button class="rounded p-1.5 {view === 'list' ? 'bg-raised text-fg' : 'text-fg-faint hover:text-fg'}" title="List" onclick={() => setView('list')}><List size={14} /></button>
  </div>
</PageHeader>

{#if sourcesError}
  <div class="panel">
    <Empty icon={Compass} title="Sources unavailable" description={sourcesError}>
      <Button size="sm" icon={RefreshCw} onclick={loadSources}>Try again</Button>
    </Empty>
  </div>
{:else if statuses.length === 0}
  <div class="panel"><Skeleton rows={4} class="p-4" /></div>
{:else}
  <div class="mb-3">
    <SourceTabs {statuses} value={sourceId} onChange={pickSourceId} />
  </div>

  <form class="mb-3 flex flex-wrap items-center gap-2" onsubmit={submit}>
    <div class="relative min-w-64 flex-1">
      <Search size={15} class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-fg-faint" />
      <input
        bind:this={input}
        class="input pr-28 pl-9"
        bind:value={query}
        oninput={onInput}
        placeholder={caps?.search ? `Search ${name}, or paste ${caps?.repoExample || 'a repository name'} to open it` : `Type ${caps?.repoExample || 'a repository name'} to open it`}
        autocomplete="off"
        spellcheck="false"
      />
      <div class="pointer-events-none absolute top-1/2 right-2 flex -translate-y-1/2 items-center gap-1.5 text-[11px] text-fg-faint">
        {#if canSubmitRepo}<span class="pointer-events-auto rounded bg-accent/15 px-1.5 py-0.5 text-accent">Enter opens it</span>{:else if !query}<span class="kbd">/</span>{/if}
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
          title={ascending ? `${direction.asc}, click for ${direction.desc.toLowerCase()}` : `${direction.desc}, click for ${direction.asc.toLowerCase()}`}
          onclick={() => (ascending = !ascending)}
        >
          {#if ascending}<ArrowUpNarrowWide size={14} /> {direction.asc}{:else}<ArrowDownWideNarrow size={14} /> {direction.desc}{/if}
        </button>
      {/if}
    {/if}
    <Button type="submit" variant="primary" loading={searching} icon={canSubmitRepo ? Eye : Search}>{canSubmitRepo ? 'Open' : 'Search'}</Button>
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

  {#if needsToken}
    <div class="mb-4 flex items-center gap-2 rounded-lg border border-warn/30 bg-warn/8 px-3 py-2 text-xs text-warn">
      <KeyRound size={13} class="shrink-0" />
      <span>{name} lets you browse freely but needs a token to download. Set <span class="font-mono">{caps?.tokenEnv || source?.tokenEnv || 'its token variable'}</span> in the daemon's environment and restart it.</span>
    </div>
  {/if}

  <div class="mb-2 flex items-center gap-3 text-xs text-fg-faint">
    {#if searching}
      <span class="inline-flex items-center gap-1.5"><Spinner size={12} /> {browsing ? 'Loading' : 'Searching'} {name}…</span>
    {:else if hits.length}
      <span>
        {#if total > 0n}{hits.length.toLocaleString()} of {Number(total).toLocaleString()}{:else}{hits.length.toLocaleString()}{/if}
        {browsing ? 'models' : 'matches'} on {name}{#if sortLabel}, {sortLabel.toLowerCase()}{ascending && reversible ? `, ${direction.asc.toLowerCase()}` : ''}{/if}
      </span>
    {/if}
  </div>

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
      {#each hits as h (h.repo)}
        <HitCard hit={h} {caps} {runtimes} compact={view === 'list'} selected={selected?.repo === h.repo && drawerOpen} onOpen={openHit} />
      {/each}
    </div>
    <div bind:this={sentinel} class="flex items-center justify-center py-6 text-xs text-fg-faint">
      {#if loadingMore}
        <span class="inline-flex items-center gap-1.5"><Spinner size={12} /> Loading more…</span>
      {:else if nextCursor}
        <Button size="sm" variant="outline" onclick={more}>Load more</Button>
      {:else if hits.length >= pageSize}
        <span>That is everything {name} listed.</span>
      {/if}
    </div>
  {/if}
{/if}

{#if selected}
  <ModelDrawer bind:open={drawerOpen} {sourceId} sourceLabel={name} repo={selected.repo} revision={selected.revision} {caps} {runtimes} hit={selected.hit} bind:slotId onNavigate={(repo, rev) => { if (selected) selected = { ...selected, repo, revision: rev, hit: hits.find((h) => h.repo === repo) ?? null }; syncUrl(); }} />
{/if}
