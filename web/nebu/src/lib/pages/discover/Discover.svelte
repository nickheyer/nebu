<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, num, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { Facet, SearchHit, SourceStatus } from '#lib/proto/nebu/v1/source_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { prefs } from '#lib/state/prefs.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import SearchBox from '#lib/ui/SearchBox.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import MultiSelect from '#lib/ui/fields/MultiSelect.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Toolbar from '#lib/ui/layout/Toolbar.svelte';
	import { paramCount } from '#lib/pages/models/models.js';
	import { modelsTabs } from '#lib/pages/models/tabs.js';
	import { compact, lastSearch, repoPath } from '#lib/pages/discover/discover.js';
	import ArrowDownIcon from 'phosphor-svelte/lib/ArrowDownIcon';
	import ArrowUpIcon from 'phosphor-svelte/lib/ArrowUpIcon';
	import CheckCircleIcon from 'phosphor-svelte/lib/CheckCircleIcon';
	import FunnelIcon from 'phosphor-svelte/lib/FunnelIcon';
	import MagnifyingGlassIcon from 'phosphor-svelte/lib/MagnifyingGlassIcon';
	import PlugsIcon from 'phosphor-svelte/lib/PlugsIcon';
	import SortAscendingIcon from 'phosphor-svelte/lib/SortAscendingIcon';

	type Params = { sourceId: string; query: string; sort: string; ascending: boolean; filters: Record<string, string>; author: string };

	const initial = page.url.searchParams;
	// Facet choices ride in the URL as f.<facet id>
	const facetPrefix = 'f.';

	let sources = $state<SourceStatus[]>([]);
	let sourcesLoaded = $state(false);
	let sourcesError = $state('');
	let sourceId = $state(initial.get('source') || prefs.discoverSource);
	let query = $state(initial.get('q') ?? '');
	let sort = $state<string | undefined>(initial.get('sort') || undefined);
	let ascending = $state(initial.get('asc') === '1');
	let author = $state(initial.get('author') ?? '');
	let filters = $state<Record<string, string[]>>(
		Object.fromEntries(
			[...initial.entries()].filter(([k, v]) => k.startsWith(facetPrefix) && v).map(([k, v]) => [k.slice(facetPrefix.length), v.split(',').filter(Boolean)])
		)
	);
	let warnings = $state<string[]>([]);
	// Filters a link carries in start out in view
	let filtersOpen = $state([...initial.entries()].some(([k, v]) => v !== '' && (k === 'author' || k.startsWith(facetPrefix))));

	const source = $derived(sources.find((s) => s.source?.id === sourceId));
	const caps = $derived(source?.capabilities);
	// The remembered source stands in as the one option until the list answers
	const sourceOptions = $derived(sources.length ? sources.map((s) => ({ value: s.source?.id ?? '', label: s.source?.name || s.source?.id || '' })) : sourceId ? [{ value: sourceId, label: prefs.discoverSourceLabel || sourceId }] : []);
	const sorts = $derived((caps?.sorts ?? []).map((s) => ({ value: s.id, label: s.label })));
	const reversible = $derived(caps?.sorts.find((s) => s.id === sort)?.reversible ?? false);
	const facets = $derived(caps?.facets ?? []);
	const authorFacet = $derived(facets.some((f) => /^author$/i.test(f.id) || /^author$/i.test(f.label)));
	const activeFilters = $derived(Object.values(filters).filter((v) => v.length).length + (author ? 1 : 0));
	const stored = $derived(new Set([...live.models.values()].map((m) => `${m.sourceId}\n${m.repo}`)));
	const warning = $derived([source?.error ?? '', caps?.authRequired && !caps.tokenPresent ? 'token missing' : '', ...warnings].filter(Boolean));

	const list = new ListQuery<SearchHit, Params>(
		async (p, cursor, limit, signal) => {
			const res = await rpc.sources.search({ sourceId: p.sourceId, query: p.query, sort: p.sort, ascending: p.ascending, filters: p.filters, author: p.author, cursor, limit }, { signal });
			if (!cursor) warnings = res.warnings;
			return { items: res.hits, next: res.nextCursor };
		},
		[],
		30
	);

	async function loadSources() {
		sourcesError = '';
		try {
			sources = (await rpc.sources.listSources({})).sources;
			if (!sources.some((s) => s.source?.id === sourceId)) {
				sourceId = sources[0]?.source?.id ?? '';
				sort = undefined;
				filters = {};
			}
			sort ??= sources.find((s) => s.source?.id === sourceId)?.capabilities?.defaultSort || undefined;
		} catch (err) {
			sourcesError = reason(err);
			toast.error('Sources failed to list', sourcesError);
		} finally {
			sourcesLoaded = true;
		}
	}

	onMount(loadSources);

	$effect(() => {
		if (!sourceId) return;
		prefs.discoverSource = sourceId;
		if (source?.source) prefs.discoverSourceLabel = source.source.name || source.source.id;
	});

	$effect(() => {
		if (!sourcesLoaded || !sourceId) return;
		const picked = Object.fromEntries(Object.entries(filters).filter(([, v]) => v.length).map(([k, v]) => [k, v.join(',')]));
		void list.load({ sourceId, query, sort: sort ?? '', ascending, filters: picked, author });
	});

	// The search lives in the URL
	$effect(() => {
		if (!sourcesLoaded || !sourceId) return;
		const url = new URL(location.pathname, location.origin);
		url.searchParams.set('source', sourceId);
		if (query) url.searchParams.set('q', query);
		if (sort) url.searchParams.set('sort', sort);
		if (ascending) url.searchParams.set('asc', '1');
		if (author) url.searchParams.set('author', author);
		for (const [k, v] of Object.entries(filters)) if (v.length) url.searchParams.set(facetPrefix + k, v.join(','));
		lastSearch.href = `${url.pathname}${url.search}`;
		if (url.search !== location.search) void goto(url, { replace: true, shallow: true, reset: false });
	});

	function sourceChanged(id: string | undefined) {
		if (!id) return;
		sourceId = id;
		sort = sources.find((s) => s.source?.id === id)?.capabilities?.defaultSort || undefined;
		ascending = false;
		filters = {};
		author = '';
	}

	function setFacet(f: Facet, values: string[]) {
		filters = { ...filters, [f.id]: values };
	}

	function clearFacets() {
		filters = {};
		author = '';
	}

	function clearFilters() {
		query = '';
		clearFacets();
	}

	function hitTip(h: SearchHit): string {
		return [
			h.downloads ? `${num(h.downloads)} downloads` : '',
			h.likes ? `${num(h.likes)} likes` : '',
			h.updatedAt ? `updated ${when(h.updatedAt)}` : '',
			h.license ? `license ${h.license}` : '',
			h.gated ? 'gated' : '',
			h.private ? 'private' : ''
		]
			.filter(Boolean)
			.join('\n');
	}

	// The formats a repository holds, each once
	function formatsOf(h: SearchHit): string {
		const seen = new Set<string>();
		return h.formats.filter((w) => w && !seen.has(w.toLowerCase()) && seen.add(w.toLowerCase())).join(', ');
	}

	// What a row says of a repository beside its name, a dash where the source states nothing
	function hitFacts(h: SearchHit) {
		return [
			{ value: h.sizeBytes ? bytes(h.sizeBytes) : '\u2014', tip: h.sizeBytes ? 'Every file of the repository together' : 'The source states no size' },
			{ value: paramCount(h.parameters) || '\u2014', tip: h.parameters ? `${num(h.parameters)} parameters` : 'The source states no parameter count' },
			{ value: h.downloads ? compact(h.downloads) : '\u2014', tip: h.downloads ? `${num(h.downloads)} downloads` : 'The source states no download count' }
		];
	}
</script>

{#snippet facetFields()}
	{#each facets as f (f.id)}
		{#if f.freeform}
			<Chips bind:value={() => filters[f.id] ?? [], (v) => setFacet(f, v)} label={f.label} compact />
		{:else}
			<MultiSelect bind:value={() => filters[f.id] ?? [], (v) => setFacet(f, v)} options={f.values.map((v) => ({ value: v.id, label: v.label, description: v.group || undefined }))} search={f.values.length > 8} label={f.label} compact />
		{/if}
	{/each}
	{#if !authorFacet}<Text value={author} label="Author" compact max={64} onchange={(v) => (author = v.trim())} />{/if}
{/snippet}

{#snippet sortIcon()}<SortAscendingIcon size={16} />{/snippet}

{#snippet toolbar()}
	<Toolbar>
		<span class="source" class:grow={viewport.mobile}><Select value={sourceId} options={sourceOptions} loading={!sourcesLoaded} onchange={sourceChanged} fit={!viewport.mobile} /></span>
		<span class="grow"><SearchBox bind:value={query} width="100%" /></span>
		<span class="sort" class:wide={viewport.mobile}><Select bind:value={sort} options={sorts} disabled={!sorts.length} icon={sortIcon} fit={!viewport.mobile} /></span>
		{#if reversible}
			<NeoButton text aria-label={ascending ? 'Ascending' : 'Descending'} onclick={() => (ascending = !ascending)}>
				{#snippet icon()}{#if ascending}<ArrowUpIcon size={20} />{:else}<ArrowDownIcon size={20} />{/if}{/snippet}
			</NeoButton>
		{/if}
		<NeoButton elevation={2} toggle checked={filtersOpen} color={filtersOpen || activeFilters ? 'primary' : undefined} aria-expanded={filtersOpen} aria-label={activeFilters ? `Filters, ${activeFilters} set` : 'Filters'} onclick={() => (filtersOpen = !filtersOpen)}>
			{#snippet icon()}<FunnelIcon size={20} weight={activeFilters ? 'fill' : 'regular'} />{/snippet}
			Filters{#if activeFilters}<span class="count">{activeFilters}</span>{/if}
		</NeoButton>
	</Toolbar>
{/snippet}

{#snippet settingsAction()}<RowAction label="Settings" href="/settings/sources" />{/snippet}

<PageHeader tabs={modelsTabs} active="discover" />

<Card flush tools={toolbar}>
	{#if !viewport.mobile && (filtersOpen || activeFilters)}
		<div class="filterbar dense">
			{#if filtersOpen}<div class="filters">{@render facetFields()}</div>{/if}
			{#if activeFilters}<span class="clear"><NeoButton text onclick={clearFacets}>Clear filters</NeoButton></span>{/if}
		</div>
	{/if}
	{#if warning.length}
		<ListRow lamp="warning" primary={warning[0] ?? ''} primaryKind="message" primaryTip={warning.join('\n')} action={settingsAction} />
	{/if}
	{#if sourcesError}
		<Empty icon={PlugsIcon} text="Sources failed to list" hint={sourcesError} action="Retry" onclick={loadSources} />
	{:else if sourcesLoaded && sources.length === 0}
		<Empty icon={PlugsIcon} text="No sources" action="Add a source" href="/settings/sources" primary />
	{:else if !sourcesLoaded || !list.settled}
		<Skeleton rows={10} dot={false} primary="40ch" secondary="28ch" facts={2} action={false} menu={false} />
	{:else if list.error}
		<Empty icon={MagnifyingGlassIcon} text="Search failed" hint={list.error} action="Retry" onclick={() => list.refresh()} />
	{:else if list.items.length === 0 && (query || activeFilters)}
		<Empty icon={MagnifyingGlassIcon} text="No results" action="Clear filters" onclick={clearFilters} />
	{:else if list.items.length === 0}
		<Empty icon={MagnifyingGlassIcon} text="No results" action="Manage sources" href="/settings/sources" />
	{:else}
		<List head={{ primary: 'Repository', secondary: 'Formats', facts: ['Size', 'Parameters', 'Downloads'] }}>
			{#each list.items as h (`${h.sourceId}\n${h.repo}`)}
				{@const inStore = stored.has(`${h.sourceId}\n${h.repo}`)}
				<ListRow
					primary={h.repo}
					primaryWidth="40ch"
					primaryTip={hitTip(h) ? `${h.repo}\n${hitTip(h)}` : h.repo}
					secondary={formatsOf(h) || '\u2014'}
					secondaryWidth="16ch"
					facts={hitFacts(h)}
					onclick={() => goto(repoPath(h.sourceId, h.repo))}
				>
					{#snippet badges()}
						{#if inStore}<Tip text="Stored" tag="span"><span class="stored success-text"><CheckCircleIcon size={16} /></span></Tip>{/if}
					{/snippet}
				</ListRow>
			{/each}
		</List>
		<LoadMore next={list.next} loading={list.loading} onmore={() => list.more()} />
	{/if}
</Card>

<!-- A narrow screen picks its filters in a dialog over the results instead of pushing them down the page -->
{#if viewport.mobile}
	<Dialog bind:open={filtersOpen} title="Filters" confirm="Show results" onconfirm={() => (filtersOpen = false)}>
		<div class="stack dense">{@render facetFields()}</div>
		{#snippet footer()}
			{#if activeFilters}<NeoButton text onclick={clearFacets}>Clear filters</NeoButton>{/if}
		{/snippet}
	</Dialog>
{/if}

<style>
	.source,
	.sort {
		display: inline-flex;
	}
	.sort.wide {
		flex: 1 1 8rem;
		min-width: 0;
	}
	.sort.wide > :global(*) {
		flex: 1 1 auto;
		min-width: 0;
	}
	.stack {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-3);
	}
	.stored {
		display: inline-flex;
	}
	/* How many filters are set rides on the button that opens them */
	.count {
		display: inline-grid;
		place-items: center;
		min-width: 18px;
		height: 18px;
		margin-left: 6px;
		padding: 0 5px;
		font-size: 11px;
		font-variant-numeric: tabular-nums;
		color: var(--neo-background-color);
		background: var(--neo-color-primary);
		border-radius: 9px;
	}
	/* The filters fill the row and the clear button ends it, under the button that opens them */
	.filterbar {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: flex-start;
		padding: 0 var(--nebu-card-pad) var(--nebu-space-3);
	}
	.filters {
		display: grid;
		flex: 1 1 auto;
		grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr));
		gap: var(--nebu-space-2);
		min-width: 0;
	}
	.clear {
		display: inline-flex;
		flex: 0 0 auto;
		align-items: center;
		min-height: var(--nebu-control);
		margin-left: auto;
	}
</style>
