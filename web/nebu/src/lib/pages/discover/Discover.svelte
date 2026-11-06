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
	import Empty from '#lib/ui/Empty.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import Pop from '#lib/ui/Pop.svelte';
	import SearchBox from '#lib/ui/SearchBox.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import MultiSelect from '#lib/ui/fields/MultiSelect.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { kindLabel, paramCount } from '#lib/pages/models/models.js';
	import { lastSearch, repoPath } from '#lib/pages/discover/discover.js';
	import ArrowDownIcon from 'phosphor-svelte/lib/ArrowDownIcon';
	import ArrowUpIcon from 'phosphor-svelte/lib/ArrowUpIcon';
	import CheckCircleIcon from 'phosphor-svelte/lib/CheckCircleIcon';
	import FunnelIcon from 'phosphor-svelte/lib/FunnelIcon';
	import WarningIcon from 'phosphor-svelte/lib/WarningIcon';

	type Params = { sourceId: string; query: string; sort: string; ascending: boolean; filters: Record<string, string>; author: string };

	const initial = page.url.searchParams;
	// Facet choices ride in the URL as f.<facet id>, comma separated
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
	let filtersOpen = $state(false);

	const source = $derived(sources.find((s) => s.source?.id === sourceId));
	const caps = $derived(source?.capabilities);
	const sourceOptions = $derived(sources.map((s) => ({ value: s.source?.id ?? '', label: s.source?.name || s.source?.id || '' })));
	const sorts = $derived((caps?.sorts ?? []).map((s) => ({ value: s.id, label: s.label })));
	const reversible = $derived(caps?.sorts.find((s) => s.id === sort)?.reversible ?? false);
	const facets = $derived(caps?.facets ?? []);
	const activeFilters = $derived(Object.values(filters).filter((v) => v.length).length + (author ? 1 : 0));
	const stored = $derived(new Set([...live.models.values()].map((m) => `${m.sourceId}\n${m.repo}`)));
	// The source's own error, a missing token, then the warnings the last search answered with
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
		if (sourceId) prefs.discoverSource = sourceId;
	});

	$effect(() => {
		if (!sourcesLoaded || !sourceId) return;
		const picked = Object.fromEntries(Object.entries(filters).filter(([, v]) => v.length).map(([k, v]) => [k, v.join(',')]));
		void list.load({ sourceId, query, sort: sort ?? '', ascending, filters: picked, author });
	});

	// The search lives in the URL so it can be shared and a repository page can link back to it
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

	// A new source has its own sorts and facets, so the old choices are dropped
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

	function clearFilters() {
		query = '';
		filters = {};
		author = '';
	}

	function hitTip(h: SearchHit): string {
		return [
			h.downloads ? `${num(h.downloads)} downloads` : '',
			h.likes ? `${num(h.likes)} likes` : '',
			h.updatedAt ? `updated ${when(h.updatedAt)}` : '',
			h.gated ? 'gated' : '',
			h.private ? 'private' : ''
		]
			.filter(Boolean)
			.join('\n');
	}

	// Kind, up to three formats, license
	function phrase(h: SearchHit): string {
		return [kindLabel(h.kind), ...h.formats.slice(0, 3), h.license].filter(Boolean).join(' · ');
	}

	function sizeOf(h: SearchHit): string {
		return h.sizeBytes ? bytes(h.sizeBytes) : paramCount(h.parameters);
	}
</script>

{#snippet facetFields()}
	{#each facets as f (f.id)}
		{#if f.freeform}
			<Chips bind:value={filters[f.id]} label={f.label} />
		{:else if f.multi}
			<MultiSelect bind:value={filters[f.id]} options={f.values.map((v) => ({ value: v.id, label: v.label, description: v.group || undefined }))} label={f.label} search={f.values.length > 8} placeholder="Any" />
		{:else}
			<Select value={filters[f.id]?.[0]} options={f.values.map((v) => ({ value: v.id, label: v.label, description: v.group || undefined }))} label={f.label} search={f.values.length > 8} placeholder="Any" clearable onchange={(v) => setFacet(f, v ? [v] : [])} />
		{/if}
	{/each}
	<Text value={author} label="Author" max={64} onchange={(v) => (author = v.trim())} />
{/snippet}

{#snippet filtersButton()}
	<NeoButton elevation={2} onclick={() => (filtersOpen = true)}>
		{#snippet icon()}<FunnelIcon size={20} />{/snippet}
		Filters{#if activeFilters}&nbsp;{activeFilters}{/if}
	</NeoButton>
{/snippet}

<Stack gap={16}>
	<Row gap={12}>
		<Cell min="11rem" max="14rem">
			<Select value={sourceId} options={sourceOptions} placeholder="Source" onchange={sourceChanged} />
		</Cell>
		<Cell min={viewport.mobile ? '100%' : '16rem'} max="32rem">
			<SearchBox bind:value={query} placeholder={caps?.repoExample ? `Search, such as ${caps.repoExample}` : 'Search'} width="100%" />
		</Cell>
		{#if sorts.length}
			<Cell min="9rem" max="12rem" push>
				<Select bind:value={sort} options={sorts} placeholder="Sort" />
			</Cell>
			{#if reversible}
				<Cell grow={false}>
					<NeoButton text aria-label={ascending ? 'Ascending' : 'Descending'} onclick={() => (ascending = !ascending)}>
						{#snippet icon()}{#if ascending}<ArrowUpIcon size={20} />{:else}<ArrowDownIcon size={20} />{/if}{/snippet}
					</NeoButton>
				</Cell>
			{/if}
		{/if}
		{#if facets.length}
			<Cell grow={false} push={!sorts.length}>
				{#if viewport.mobile}
					{@render filtersButton()}
				{:else}
					<Pop placement="bottom-end" width="22rem">
						{#snippet content()}{@render facetFields()}{/snippet}
						<NeoButton elevation={2}>
							{#snippet icon()}<FunnelIcon size={20} />{/snippet}
							Filters{#if activeFilters}&nbsp;{activeFilters}{/if}
						</NeoButton>
					</Pop>
				{/if}
			</Cell>
		{/if}
	</Row>
	{#if warning.length}
		<Row gap={12}>
			<Cell grow={false}><span class="warning-text warn"><WarningIcon size={20} /></span></Cell>
			<Cell min="8rem"><Tip text={warning.join('\n')} tag="span"><span class="ellipsis">{warning[0]}</span></Tip></Cell>
			<Cell grow={false} push><NeoButton text href="/settings/sources">Settings</NeoButton></Cell>
		</Row>
	{/if}
	{#if sourcesError}
		<Empty text={sourcesError} action="Retry" onclick={loadSources} />
	{:else if sourcesLoaded && sources.length === 0}
		<Empty text="No sources" action="Settings" href="/settings/sources" />
	{:else if !sourcesLoaded || !list.settled}
		<Skeleton rows={8} />
	{:else if list.error}
		<Empty text={list.error} action="Retry" onclick={() => list.refresh()} />
	{:else if list.items.length === 0}
		{#if query || activeFilters}
			<Empty text="No results" action="Clear filters" onclick={clearFilters} />
		{:else}
			<Empty text="No results" />
		{/if}
	{:else}
		<Stack gap={0}>
			{#each list.items as h (`${h.sourceId}\n${h.repo}`)}
				{@const inStore = stored.has(`${h.sourceId}\n${h.repo}`)}
				<ListRow primary={h.repo} primaryKind="repo" primaryTip={hitTip(h) ? `${h.repo}\n${hitTip(h)}` : h.repo} secondary={phrase(h)} facts={[{ value: sizeOf(h) }]} href={repoPath(h.sourceId, h.repo)}>
					{#snippet badges()}
						{#if inStore}<Tip text="In the store" tag="span"><span class="stored success-text"><CheckCircleIcon size={16} /></span></Tip>{/if}
					{/snippet}
					{#snippet action()}<NeoButton text href={repoPath(h.sourceId, h.repo)}>Open</NeoButton>{/snippet}
				</ListRow>
			{/each}
		</Stack>
		<LoadMore next={list.next} loading={list.loading} onmore={() => list.more()} />
	{/if}
</Stack>

<Sheet bind:open={filtersOpen}>
	<PanelHeader title="Filters" onclose={() => (filtersOpen = false)} />
	<Stack gap={12}>{@render facetFields()}</Stack>
</Sheet>

<style>
	.warn,
	.stored {
		display: inline-flex;
	}
</style>
