<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { replaceState } from '$app/navigation';
  import { api, message } from '$lib/api';
  import { cached, refreshCached, live, clock } from '$lib/state.svelte';
  import { fail } from '$lib/toast.svelte';
  import { facetValueLabel, groupByProvider, groupLabel, hitChips, hitSize, kindParam, locked, looksLikeRepo, parseKind, pickGroup, sortReversible, sourceLabels } from '$lib/catalog';
  import { ago, storage, count, params as fmtParams } from '$lib/format';
  import { SourceKind, type Provider, type SearchHit, type SourceStatus } from '$proto/source_pb';
  import { ArrowDown, ArrowUp, KeyRound, X, RefreshCw, Plus, Lock, EyeOff, Check, ChevronDown } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Tip from '$lib/components/ui/Tip.svelte';
  import Select from '$lib/components/ui/Select.svelte';
  import SearchInput from '$lib/components/ui/SearchInput.svelte';
  import ModelsNav from '$lib/components/ModelsNav.svelte';
  import SourceRail from '$lib/components/catalog/SourceRail.svelte';
  import FacetPicker from '$lib/components/catalog/FacetPicker.svelte';
  import ModelDrawer from '$lib/components/ModelDrawer.svelte';
  import SourceDrawer from '$lib/components/SourceDrawer.svelte';

  const pageSize = 30;

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
  let showWarnings = $state(false);
  let searching = $state(false);
  let loadingMore = $state(false);
  let searchError = $state('');
  let selected = $state<{ sourceId: string; repo: string; revision: string; hit: SearchHit | null } | null>(null);
  let drawerOpen = $state(false);
  let slotId = $state('');
  let input: HTMLInputElement | undefined = $state();
  let sentinel: HTMLDivElement | undefined = $state();
  let debounce: ReturnType<typeof setTimeout> | null = null;
  let generation = 0;
  let booted = false;
  // The URL this page last wrote, so one it did not write is read as a new request
  let written = '';
  // Sources are added and edited from the rail, the form the provider describes
  let providers = $state<Provider[]>([]);
  let sourceOpen = $state(false);
  let editing = $state<SourceStatus | null>(null);

  function editSource(s: SourceStatus) {
    editing = s;
    sourceOpen = true;
  }
  function addSource() {
    editing = null;
    sourceOpen = true;
  }
  $effect(() => {
    api.sources
      .listProviders({})
      .then((p) => (providers = p.providers))
      .catch((err) => fail(err, 'Could not list providers'));
  });

  const statuses = $derived(cached.sources);
  const groups = $derived(groupByProvider(statuses));
  // Every source at once, the entry before the providers
  const all = $derived(kind === SourceKind.UNSPECIFIED && sourceId === '' && groups.length > 0);
  const group = $derived(all ? undefined : pickGroup(groups, kind, sourceId));
  // One source answers for the provider's capabilities, the chosen one or the first that works
  const status = $derived(group?.sources.find((s) => s.source?.id === sourceId) ?? group?.sources.find((s) => !s.error) ?? group?.sources[0]);
  const caps = $derived(status?.capabilities);
  const capsOf = (id: string) => statuses.find((s) => s.source?.id === id)?.capabilities ?? caps;
  const merged = $derived(all || (!!group && sourceId === '' && group.sources.length > 1));
  const labels = $derived(sourceLabels(all ? statuses : (group?.sources ?? [])));
  const name = $derived(all ? 'all sources' : group ? (merged ? group.name : (labels.get(sourceId) ?? groupLabel(group))) : 'the catalog');
  const effectiveSort = $derived(sort || caps?.defaultSort || '');
  const reversible = $derived(sortReversible(caps, effectiveSort));
  const activeFilters = $derived(Object.entries(filters).filter(([, v]) => v));
  const repoSource = $derived(all ? statuses.find((s) => !s.error && looksLikeRepo(s.capabilities, query)) : looksLikeRepo(caps, query) ? status : undefined);
  const canSubmitRepo = $derived(!!repoSource);
  const tokenless = $derived((all ? statuses : merged ? (group?.sources ?? []) : status ? [status] : []).filter((s) => s.capabilities?.authRequired && !s.capabilities.tokenPresent));
  const browsing = $derived(!query.trim() && activeFilters.length === 0);
  const searchable = $derived(all || (browsing ? !!caps?.browse : !!caps?.search));
  const paginates = $derived(all ? statuses.some((s) => s.capabilities?.paginate) : !!caps?.paginate);
  const inputDead = $derived(!all && !!caps && !caps.search && !caps.repoPattern);
  const openSourceId = $derived(sourceId || status?.source?.id || statuses.find((s) => !s.error)?.source?.id || '');
  const stored = $derived(new Set([...live.models.values()].map((m) => `${m.sourceId}/${m.repo}`)));
  // Columns whose header can order the list, when the source has a sort of that id
  const columnSort = (id: string) => (!all && caps?.sorts.some((s) => s.id === id) ? id : '');
  const sortItems = $derived((caps?.sorts ?? []).map((s) => ({ value: s.id === caps?.defaultSort ? '' : s.id, label: s.label })));

  // The URL picks the source, query, sort, and filters once the cached sources are in
  $effect(() => {
    if (!cached.loaded || booted || statuses.length === 0) return;
    untrack(boot);
  });

  // A link into the catalog, a model's namespace say, is read the same way
  $effect(() => {
    const here = page.url.pathname + page.url.search;
    untrack(() => {
      if (booted && here !== written) boot();
    });
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
    booted = true;
    search();
    const repo = p.get('repo');
    if (repo) openRepo(openSourceId, repo, p.get('rev') ?? '', null);
    else drawerOpen = false;
  }

  // A provider only accepts its own sorts and facets, so switching drops the rest
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
    written = next;
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
      // A model opened by URL before the list arrived picks up its listing now
      if (selected && !selected.hit) selected = { ...selected, hit: hits.find((h) => h.repo === selected!.repo && (h.sourceId || openSourceId) === selected!.sourceId) ?? null };
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

  // Loads the next page as the last row scrolls into view
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

  function submit() {
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

  // Clicking a column orders by it, the same column again flips it where the source allows
  function orderBy(id: string) {
    if (!id) return;
    if (effectiveSort === id) {
      if (sortReversible(caps, id)) ascending = !ascending;
      return;
    }
    sort = id === caps?.defaultSort ? '' : id;
    ascending = false;
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

  function clearAll() {
    const wasClean = !query && !sort && !ascending && activeFilters.length === 0;
    query = '';
    filters = {};
    sort = '';
    ascending = false;
    if (wasClean) search();
  }

  function sizeOf(h: SearchHit): string {
    const s = hitSize(h);
    return s.kind === 'params' ? fmtParams(s.value) : s.kind === 'bytes' ? storage(s.value, 1) : '–';
  }
</script>

{#snippet th(label: string, id = '', num = false)}
  {@const sortId = columnSort(id)}
  {@const on = !!sortId && effectiveSort === sortId}
  <th class={num ? 'num' : ''} aria-sort={on ? (ascending ? 'ascending' : 'descending') : 'none'}>
    {#if sortId}
      <button type="button" class="caps inline-flex items-center gap-1 rounded-sm transition-colors hover:text-fg {on ? 'text-fg' : ''}" onclick={() => orderBy(sortId)}>
        {label}
        {#if on}{#if ascending}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}{/if}
      </button>
    {:else}
      {label}
    {/if}
  </th>
{/snippet}

{#snippet skeletonRows(n: number)}
  {#each Array(n) as _, i (i)}
    <tr aria-busy="true">
      <td><div class="skeleton h-3" style="width: {55 + ((i * 7) % 30)}%"></div><div class="skeleton mt-2 h-2.5" style="width: {30 + ((i * 11) % 25)}%"></div></td>
      {#if merged}<td><div class="skeleton h-3 w-16"></div></td>{/if}
      <td><div class="skeleton h-3 w-20"></div></td>
      <td><div class="skeleton h-3 w-14"></div></td>
      <td class="num"><div class="skeleton ml-auto h-3 w-10"></div></td>
      <td class="num"><div class="skeleton ml-auto h-3 w-12"></div></td>
      <td class="num"><div class="skeleton ml-auto h-3 w-8"></div></td>
      <td class="num"><div class="skeleton ml-auto h-3 w-12"></div></td>
    </tr>
  {/each}
{/snippet}

<PageHeader title="Models">
  {#snippet below()}
    <ModelsNav />
  {/snippet}
</PageHeader>

{#if cached.error && statuses.length === 0}
  <Empty title={cached.error}>
    <Button size="sm" icon={RefreshCw} onclick={() => refreshCached()}>Retry</Button>
  </Empty>
{:else if !cached.loaded}
  <div class="grid grid-cols-1 gap-8 lg:grid-cols-[12rem_minmax(0,1fr)]">
    <div class="flex flex-col gap-2 pt-1">{#each [1, 2, 3, 4, 5] as i (i)}<div class="skeleton h-7" style="width: {60 + ((i * 13) % 35)}%"></div>{/each}</div>
    <table class="tbl"><tbody>{@render skeletonRows(8)}</tbody></table>
  </div>
{:else if statuses.length === 0}
  <Empty title="No sources">
    <Button size="sm" icon={Plus} onclick={addSource} disabled={!providers.length}>Add source</Button>
  </Empty>
{:else}
  <div class="grid grid-cols-1 gap-8 lg:grid-cols-[13rem_minmax(0,1fr)]">
    <aside class="lg:sticky lg:top-8 lg:self-start">
      <SourceRail {groups} {kind} {sourceId} onChange={pick} onEdit={editSource} onAdd={addSource} />
    </aside>

    <div class="min-w-0">
      <div class="flex flex-wrap items-center gap-2">
        <SearchInput
          class="min-w-64 flex-1"
          size="lg"
          bind:value={query}
          bind:element={input}
          oninput={onInput}
          onsubmit={submit}
          disabled={inputDead}
          placeholder={inputDead ? `${name} opens nothing by name` : caps?.search || all ? `Search ${name}` : `Open ${caps?.repoExample || 'owner/name'}`}
        >
          {#snippet trailing()}
            {#if canSubmitRepo}<span class="rounded-sm bg-accent/15 px-1.5 text-[11px] text-accent">Enter opens it</span>{/if}
          {/snippet}
        </SearchInput>
        {#if sortItems.length && !all}
          <Select class="w-40" bind:value={sort} label="Sort" items={sortItems} />
          {#if reversible}
            <IconButton size="lg" variant="secondary" icon={ascending ? ArrowUp : ArrowDown} label={ascending ? 'Ascending' : 'Descending'} onclick={() => (ascending = !ascending)} />
          {/if}
        {/if}
      </div>
      {#if (caps?.facets.length && !all) || activeFilters.length || query || sort}
        <div class="mt-2 flex flex-wrap items-center gap-1.5">
          {#if caps?.facets.length && !all}
            {#each caps.facets as f (f.id)}
              <FacetPicker facet={f} value={filters[f.id] ?? ''} onChange={(v) => (filters = { ...filters, [f.id]: v })} />
            {/each}
          {/if}
          {#if activeFilters.length || query || sort}
            <Button type="button" variant="ghost" size="sm" icon={X} onclick={clearAll}>Clear</Button>
          {/if}
        </div>
      {/if}

      <div class="mt-4 mb-1 flex min-h-5 flex-wrap items-center gap-x-4 gap-y-1 text-xs text-fg-faint">
        {#if !searching && hits.length}
          <span class="tabular-nums">{#if total > 0n}{hits.length.toLocaleString()} of {Number(total).toLocaleString()}{:else}{hits.length.toLocaleString()}{/if}</span>
        {/if}
        {#each tokenless as s (s.source?.id)}
          <span class="inline-flex items-center gap-1.5 text-warn"><KeyRound size={12} />{merged ? labels.get(s.source?.id ?? '') : name} downloads need <span class="font-mono">{s.capabilities?.tokenEnv || 'a token'}</span></span>
        {/each}
        {#if status?.error && !merged}
          <span class="text-bad">{status.error}</span>
        {/if}
        {#if warnings.length}
          <button type="button" class="inline-flex items-center gap-1 text-warn hover:underline" onclick={() => (showWarnings = !showWarnings)}>
            {warnings.length} {warnings.length === 1 ? 'source' : 'sources'} did not answer<ChevronDown size={12} class="transition-transform {showWarnings ? 'rotate-180' : ''}" />
          </button>
        {/if}
      </div>
      {#if showWarnings && warnings.length}
        <ul class="note note-warn mb-3 list-disc pl-6">
          {#each warnings as w, i (i)}<li>{w}</li>{/each}
        </ul>
      {/if}

      {#if searchError}
        <Empty compact title={searchError}>
          <Button size="sm" icon={RefreshCw} onclick={search}>Retry</Button>
        </Empty>
      {:else if !searching && hits.length === 0}
        {#if !caps?.browse && browsing && !all}
          <Empty compact title="Type {caps?.repoExample || 'owner/name'} to open it" />
        {:else if !searchable}
          <Empty compact title="{name} does not search" />
        {:else}
          <Empty compact title="No results">
            {#if !browsing}<Button size="sm" variant="ghost" onclick={clearAll}>Clear</Button>{/if}
          </Empty>
        {/if}
      {:else}
        <table class="tbl table-fixed">
          <colgroup>
            <col />
            {#if merged}<col class="w-28" />{/if}
            <col class="w-44" />
            <col class="w-28" />
            <col class="w-20" />
            <col class="w-24" />
            <col class="w-20" />
            <col class="w-24" />
          </colgroup>
          <thead>
            <tr>
              {@render th('Model', 'name')}
              {#if merged}<th>Source</th>{/if}
              <th>Task</th>
              <th>Format</th>
              {@render th('Size', '', true)}
              {@render th('Downloads', 'downloads', true)}
              {@render th('Likes', 'likes', true)}
              {@render th('Updated', 'updated', true)}
            </tr>
          </thead>
          <tbody>
            {#if searching}
              {@render skeletonRows(10)}
            {:else}
              {#each hits as h (h.sourceId + '/' + h.repo)}
                {@const hcaps = capsOf(h.sourceId)}
                {@const title = h.name && h.name !== h.repo ? h.name : h.repo}
                {@const chips = hitChips(h, hcaps)}
                {@const on = selected?.repo === h.repo && selected?.sourceId === h.sourceId && drawerOpen}
                <tr class="row-link {on ? 'row-active' : ''}" onclick={() => openHit(h)}>
                  <td>
                    <div class="flex items-center gap-2">
                      <span class="truncate text-fg" title={h.repo}>{title}</span>
                      {#if locked(h, hcaps)}<Tip text="Gated. Downloads need {hcaps?.tokenEnv || 'a token'}"><Lock size={12} class="shrink-0 text-warn" /></Tip>{:else if h.gated}<Tip text="Gated. The token on this source has access"><Lock size={12} class="shrink-0 text-fg-faint" /></Tip>{/if}
                      {#if h.private}<Tip text="Private"><EyeOff size={12} class="shrink-0 text-fg-faint" /></Tip>{/if}
                      {#if stored.has(h.sourceId + '/' + h.repo)}<Tip text="In the library"><Check size={12} class="shrink-0 text-ok" /></Tip>{/if}
                    </div>
                    <div class="truncate text-xs text-fg-faint">{h.author}{#if h.name && h.name !== h.repo}<span class="font-mono">{' · '}{h.repo}</span>{/if}</div>
                    {#if chips.length}
                      <div class="mt-1 flex flex-wrap gap-1">
                        {#each chips as c (c)}<span class="rounded-sm bg-raised px-1.5 text-[11px] text-fg-muted">{c}</span>{/each}
                      </div>
                    {/if}
                  </td>
                  {#if merged}<td class="truncate text-fg-muted">{labels.get(h.sourceId) ?? h.sourceId}</td>{/if}
                  <td class="truncate text-fg-muted">{h.task ? facetValueLabel(hcaps, 'task', h.task) : '–'}</td>
                  <td class="truncate font-mono text-xs text-fg-muted">{h.formats.join(' ') || '–'}</td>
                  <td class="num">{sizeOf(h)}</td>
                  <td class="num text-fg-muted">{h.downloads > 0n ? count(h.downloads) : '–'}</td>
                  <td class="num text-fg-muted">{h.likes > 0n ? count(h.likes) : '–'}</td>
                  <td class="num text-fg-muted whitespace-nowrap">{h.updatedAt ? ago(h.updatedAt, clock.now) : '–'}</td>
                </tr>
              {/each}
              {#if loadingMore}
                {@render skeletonRows(4)}
              {/if}
            {/if}
          </tbody>
        </table>
        {#if !searching && nextCursor && paginates}
          <div bind:this={sentinel} class="flex justify-center py-4">
            <Button size="sm" variant="ghost" loading={loadingMore} onclick={more}>More</Button>
          </div>
        {/if}
      {/if}
    </div>
  </div>
{/if}

<SourceDrawer bind:open={sourceOpen} {providers} {editing} />

{#if selected}
  <ModelDrawer
    bind:open={drawerOpen}
    sourceId={selected.sourceId}
    sourceLabel={labels.get(selected.sourceId) ?? name}
    repo={selected.repo}
    revision={selected.revision}
    caps={capsOf(selected.sourceId)}
    hit={selected.hit}
    bind:slotId
    onNavigate={(repo, rev) => {
      if (selected) selected = { ...selected, repo, revision: rev, hit: hits.find((h) => h.repo === repo) ?? null };
      syncUrl();
    }}
  />
{/if}
