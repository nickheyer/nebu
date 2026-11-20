<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { onMount, untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoPill } from '#lib/neo/pill/index.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { num, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { FacetInput, FacetSchema, type Facet, type SearchHit } from '#lib/proto/nebu/v1/source_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { catalog } from '#lib/state/catalog.svelte.js';
	import { prefs } from '#lib/state/prefs.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Count from '#lib/ui/Count.svelte';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import Pop from '#lib/ui/Pop.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import SearchBox from '#lib/ui/SearchBox.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Options from '#lib/ui/fields/Options.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Toolbar from '#lib/ui/layout/Toolbar.svelte';
	import type { ListHead } from '#lib/ui/list.svelte.js';
	import { modelsTabs } from '#lib/pages/models/tabs.js';
	import FilterPanel from '#lib/pages/discover/FilterPanel.svelte';
	import { cell, facetText, lastSearch, repoPath, wordy } from '#lib/pages/discover/discover.js';
	import ArrowDownIcon from 'phosphor-svelte/lib/ArrowDownIcon';
	import ArrowUpIcon from 'phosphor-svelte/lib/ArrowUpIcon';
	import CheckCircleIcon from 'phosphor-svelte/lib/CheckCircleIcon';
	import ColumnsIcon from 'phosphor-svelte/lib/ColumnsIcon';
	import FunnelIcon from 'phosphor-svelte/lib/FunnelIcon';
	import MagnifyingGlassIcon from 'phosphor-svelte/lib/MagnifyingGlassIcon';
	import PlugsIcon from 'phosphor-svelte/lib/PlugsIcon';
	import SortAscendingIcon from 'phosphor-svelte/lib/SortAscendingIcon';

	type Params = { sourceId: string; query: string; sort: string; ascending: boolean; filters: Record<string, string>; author: string };

	const initial = page.url.searchParams;
	// Facet choices ride in the URL as f.<facet id>
	const facetPrefix = 'f.';

	// The sources come from the catalog the browser keeps, so the toolbar and its filters stand at once
	const sources = $derived(catalog.sources);
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
	let filtersOpen = $state(false);
	// The id of the facet the filter panel shows
	let facetInView = $state('');

	const source = $derived(sources.find((s) => s.source?.id === sourceId));
	// Whether the catalog lists the source, which a search waits on
	const known = $derived(source !== undefined);
	const caps = $derived(source?.capabilities);
	// The remembered source stands in as the one option until the list answers
	const sourceOptions = $derived(sources.length ? sources.map((s) => ({ value: s.source?.id ?? '', label: s.source?.name || s.source?.id || '' })) : sourceId ? [{ value: sourceId, label: prefs.discoverSourceLabel || sourceId }] : []);
	const sorts = $derived((caps?.sorts ?? []).map((s) => ({ value: s.id, label: s.label })));
	const reversible = $derived(caps?.sorts.find((s) => s.id === sort)?.reversible ?? false);
	const facets = $derived(caps?.facets ?? []);
	const columns = $derived(caps?.columns ?? []);
	// The columns in view: the person's pick for this source, else the ones the source shows first
	const picked = $derived(prefs.discoverColumns[sourceId]);
	const chosen = $derived(columns.filter((c) => (picked ? picked.includes(c.id) : c.shown)));
	// A narrow row has room for one column beside the name: the one the results are ordered by, else the first of numbers
	const lead = $derived(viewport.mobile ? (chosen.find((c) => c.sort && c.sort === sort) ?? chosen.find((c) => !wordy(c))) : undefined);
	const shown = $derived(lead ? [lead, ...chosen.filter((c) => c !== lead)] : chosen);
	// A heading orders the results where the source has an order by its column
	const head = $derived<ListHead>({
		primary: 'Repository',
		facts: shown.map((c) => ({
			label: c.label,
			tip: c.description || undefined,
			start: wordy(c),
			sorted: c.sort && c.sort === sort ? (ascending ? 'ascending' : 'descending') : undefined,
			onsort: c.sort ? () => sortBy(c.sort) : undefined
		}))
	});
	// A source with no author filter of its own takes one through the search's author field, offered as one more facet
	const authorFacet = create(FacetSchema, { id: '\nauthor', label: 'Author', input: FacetInput.TEXT });
	const offered = $derived(facets.some((f) => /^author$/i.test(f.id) || /^author$/i.test(f.label)) ? facets : [...facets, authorFacet]);
	// Every value set by facet id, the author among them
	const held = $derived<Record<string, string[]>>(author ? { ...filters, [authorFacet.id]: [author] } : filters);
	// The filters set, each a chip. One the source does not offer keeps a chip under its id, so it can still be taken off.
	const applied = $derived([
		...offered.filter((f) => held[f.id]?.length).map((f) => ({ facet: f, text: facetText(f, held[f.id] ?? []) })),
		...Object.entries(filters)
			.filter(([id, v]) => v.length && !offered.some((f) => f.id === id))
			.map(([id, v]) => ({ facet: create(FacetSchema, { id, label: id, input: FacetInput.TEXT }), text: v.join(', ') }))
	]);
	const activeFilters = $derived(applied.length);
	const stored = $derived(new Set([...live.models.values()].map((m) => `${m.sourceId}\n${m.repo}`)));
	const warning = $derived([source?.error ?? '', caps?.authRequired && !caps.tokenPresent ? (caps.credentials.length ? 'no credential' : 'token missing') : '', ...warnings].filter(Boolean));

	const list = new ListQuery<SearchHit, Params>(
		async (p, cursor, limit, signal) => {
			const res = await rpc.sources.search({ sourceId: p.sourceId, query: p.query, sort: p.sort, ascending: p.ascending, filters: p.filters, author: p.author, cursor, limit }, { signal });
			if (!cursor) warnings = res.warnings;
			return { items: res.hits, next: res.nextCursor };
		},
		[],
		30
	);

	onMount(() => void catalog.load());

	// A source the daemon no longer lists gives way to the first, and a search with no order takes its source's own.
	// A kept catalog may be missing a source added since, so the source in view stands until the daemon has answered.
	$effect(() => {
		if (!catalog.loaded) return;
		const list = sources;
		const settled = catalog.settled;
		untrack(() => {
			if (!list.some((s) => s.source?.id === sourceId)) {
				if (!settled) return;
				sourceId = list[0]?.source?.id ?? '';
				sort = undefined;
				filters = {};
				author = '';
			}
			sort ??= list.find((s) => s.source?.id === sourceId)?.capabilities?.defaultSort || undefined;
		});
	});

	$effect(() => {
		if (!sourceId) return;
		prefs.discoverSource = sourceId;
		if (source?.source) prefs.discoverSourceLabel = source.source.name || source.source.id;
	});

	$effect(() => {
		if (!known) return;
		const picked = Object.fromEntries(Object.entries(filters).filter(([, v]) => v.length).map(([k, v]) => [k, v.join(',')]));
		void list.load({ sourceId, query, sort: sort ?? '', ascending, filters: picked, author });
	});

	// The search lives in the URL
	$effect(() => {
		if (!catalog.loaded || !sourceId) return;
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

	// The runtimes column names the daemon's runtimes, asked for once the column is first in view
	$effect(() => {
		if (shown.some((c) => c.id === 'runtimes')) catalog.loadRuntimes().catch((err) => toast.error('Runtimes failed to list', reason(err)));
	});

	// An order the source cannot turn round runs the one way it has
	$effect(() => {
		if (caps && !reversible) ascending = false;
	});

	// Orders by a column, and turns the order round on a second press where the source can
	function sortBy(id: string) {
		if (sort !== id) {
			sort = id;
			ascending = false;
		} else if (reversible) ascending = !ascending;
	}

	function setColumns(ids: string[]) {
		prefs.discoverColumns = { ...prefs.discoverColumns, [sourceId]: ids };
	}

	function resetColumns() {
		prefs.discoverColumns = Object.fromEntries(Object.entries(prefs.discoverColumns).filter(([id]) => id !== sourceId));
	}

	function sourceChanged(id: string | undefined) {
		if (!id) return;
		sourceId = id;
		sort = sources.find((s) => s.source?.id === id)?.capabilities?.defaultSort || undefined;
		ascending = false;
		filters = {};
		author = '';
	}

	function setFacet(f: Facet, values: string[]) {
		if (f === authorFacet) author = values[0] ?? '';
		else filters = { ...filters, [f.id]: values };
	}

	// Opens the filters on one facet, on their names when none is given
	function showFilters(f?: Facet) {
		facetInView = f?.id ?? '';
		filtersOpen = true;
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
</script>

{#snippet filterPanel()}
	<FilterPanel facets={offered} filters={held} bind:active={facetInView} stacked={viewport.mobile} onset={setFacet} />
{/snippet}

{#snippet filtersButton()}
	<NeoButton elevation={2} toggle checked={filtersOpen} color={filtersOpen || activeFilters ? 'primary' : undefined} aria-expanded={filtersOpen} aria-label={activeFilters ? `Filters, ${activeFilters} set` : 'Filters'} onclick={viewport.mobile ? () => showFilters() : undefined}>
		{#snippet icon()}<FunnelIcon size={20} weight={activeFilters ? 'fill' : 'regular'} />{/snippet}
		Filters{#if activeFilters}<Count n={activeFilters} after />{/if}
	</NeoButton>
{/snippet}

{#snippet sortIcon()}<SortAscendingIcon size={16} />{/snippet}

{#snippet columnPicker()}
	<div class="columns">
		<Options bind:value={() => chosen.map((c) => c.id), setColumns} options={columns.map((c) => ({ value: c.id, label: c.label, tip: c.description || undefined }))} label="Columns" />
		{#if picked}<NeoButton text onclick={resetColumns}>Reset to Defaults</NeoButton>{/if}
	</div>
{/snippet}

{#snippet toolbar()}
	<Toolbar>
		<span class="source" class:grow={viewport.mobile}><Select value={sourceId} options={sourceOptions} loading={!catalog.loaded} onchange={sourceChanged} fit={!viewport.mobile} /></span>
		<span class="grow"><SearchBox bind:value={query} width="100%" /></span>
		<span class="sort" class:wide={viewport.mobile}><Select bind:value={sort} options={sorts} disabled={!sorts.length} icon={sortIcon} fit={!viewport.mobile} /></span>
		{#if reversible}
			<NeoButton text aria-label={ascending ? 'Ascending' : 'Descending'} onclick={() => (ascending = !ascending)}>
				{#snippet icon()}{#if ascending}<ArrowUpIcon size={20} />{:else}<ArrowDownIcon size={20} />{/if}{/snippet}
			</NeoButton>
		{/if}
		<!-- A narrow screen picks its filters in a dialog, a wide one in a panel under the button -->
		{#if viewport.mobile}
			{@render filtersButton()}
		{:else}
			<Pop bind:open={filtersOpen} placement="bottom-end" bare content={filterPanel}>{@render filtersButton()}</Pop>
		{/if}
		{#if columns.length}
			<Pop placement="bottom-end" width="18rem" content={columnPicker}>
				<NeoButton elevation={2} aria-label="Columns">
					{#snippet icon()}<ColumnsIcon size={20} />{/snippet}
					{#if !viewport.mobile}Columns{/if}
				</NeoButton>
			</Pop>
		{/if}
	</Toolbar>
{/snippet}

{#snippet settingsAction()}
	{#if caps?.credentials.length}<RowAction label="Profile" href="/profile" />{:else}<RowAction label="Settings" href="/settings/sources" />{/if}
{/snippet}

<PageHeader tabs={modelsTabs} active="discover" />

<Card flush tools={toolbar}>
	{#if applied.length}
		<!-- What is filtered by stays in view, each filter one press from changing and one from gone -->
		<div class="applied dense">
			{#each applied as a (a.facet.id)}
				<NeoPill close onClose={() => setFacet(a.facet, [])} affixProps={{ closeProps: { 'aria-label': `Remove the ${a.facet.label} filter` } }}>
					<button type="button" class="chip" onclick={() => showFilters(a.facet)}>
						<span class="what">{a.facet.label}</span>
						{#if a.text}<span class="is">{a.text}</span>{/if}
					</button>
				</NeoPill>
			{/each}
			<NeoButton text onclick={clearFacets}>Clear all</NeoButton>
		</div>
	{/if}
	{#if warning.length}
		<ListRow lamp="warning" primary={warning[0] ?? ''} primaryKind="message" primaryTip={warning.join('\n')} action={settingsAction} />
	{/if}
	{#if catalog.error && sources.length === 0}
		<Empty icon={PlugsIcon} text="Sources failed to list" hint={catalog.error} action="Retry" onclick={() => catalog.refresh()} />
	{:else if catalog.loaded && sources.length === 0}
		<Empty icon={PlugsIcon} text="No sources" action="Add a source" href="/settings/sources" primary />
	{:else if !list.settled}
		<Skeleton rows={10} dot={false} primary="40ch" secondary={false} facts={shown.length || 4} action={false} menu={false} />
	{:else if list.error}
		<Empty icon={MagnifyingGlassIcon} text="Search failed" hint={list.error} action="Retry" onclick={() => list.refresh()} />
	{:else if list.items.length === 0 && (query || activeFilters)}
		<Empty icon={MagnifyingGlassIcon} text="No results" action="Clear filters" onclick={clearFilters} />
	{:else if list.items.length === 0}
		<Empty icon={MagnifyingGlassIcon} text="No results" action="Manage sources" href="/settings/sources" />
	{:else}
		<!-- More columns than the card is wide scroll sideways under it -->
		<div class="results">
			<List {head}>
				{#each list.items as h (`${h.sourceId}\n${h.repo}`)}
					{@const inStore = stored.has(`${h.sourceId}\n${h.repo}`)}
					<ListRow primary={h.repo} primaryWidth="40ch" primaryTip={hitTip(h) ? `${h.repo}\n${hitTip(h)}` : h.repo} facts={shown.map((c) => cell(h, c, catalog.runtimeNames))} onclick={() => goto(repoPath(h.sourceId, h.repo))}>
						{#snippet badges()}
							{#if inStore}<Tip text="Stored" tag="span"><span class="stored success-text"><CheckCircleIcon size={16} /></span></Tip>{/if}
						{/snippet}
					</ListRow>
				{/each}
			</List>
		</div>
		<LoadMore next={list.next} loading={list.loading} onmore={() => list.more()} />
	{/if}
</Card>

{#if viewport.mobile}
	<Dialog bind:open={filtersOpen} title="Filters" confirm="Show results" onconfirm={() => (filtersOpen = false)}>
		{@render filterPanel()}
		{#snippet footer()}
			{#if activeFilters}<NeoButton text onclick={clearFacets}>Clear all</NeoButton>{/if}
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
	.stored {
		display: inline-flex;
	}
	/* The filters set run under the toolbar, wrapping when they outgrow the row */
	.applied {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-2);
		align-items: center;
		padding: 0 var(--nebu-card-pad) var(--nebu-space-3);
	}
	/* A chip names its facet quietly and what it is set to plainly, the whole of it opening that facet */
	.chip {
		display: inline-flex;
		gap: 6px;
		align-items: baseline;
		max-width: 28rem;
		padding: 0;
		font: inherit;
		font-size: var(--nebu-text-secondary);
		color: inherit;
		cursor: pointer;
		background: none;
		border: 0;
		border-radius: 2px;
	}
	.what {
		flex: 0 0 auto;
		color: var(--nebu-text-muted);
	}
	.chip:hover .what {
		color: var(--neo-color-primary);
	}
	.is {
		min-width: 0;
		overflow: hidden;
		white-space: nowrap;
		text-overflow: ellipsis;
	}
	/* The column list scrolls under its search box inside the room the window leaves */
	.columns {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		max-height: min(30rem, calc(100dvh - 15rem));
		min-height: 0;
	}
	.results {
		overflow-x: auto;
	}
</style>
