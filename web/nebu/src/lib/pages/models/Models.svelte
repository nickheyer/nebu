<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, enumOptions } from '#lib/format.js';
	import { live, modelKey } from '#lib/live/events.svelte.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { ModelKind, ModelKindSchema, type Format } from '#lib/proto/nebu/v1/model_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { ModelSort, type StoredModel } from '#lib/proto/nebu/v1/store_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import SearchBox from '#lib/ui/SearchBox.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import ModelPanel from '#lib/pages/models/ModelPanel.svelte';
	import StoreCard from '#lib/pages/models/StoreCard.svelte';
	import { exportModel, kindLabel, launchHref, modelKeyOf, modelPath, paramCount, quality, removeModel, runtimeNames, sameModel, selectWidth, verifyModel } from '#lib/pages/models/models.js';
	import { modelsTabs } from '#lib/pages/models/tabs.js';
	import { terminal } from '#lib/pages/serve/serve.js';
	import CubeIcon from 'phosphor-svelte/lib/CubeIcon';
	import FunnelIcon from 'phosphor-svelte/lib/FunnelIcon';
	import MagnifyingGlassIcon from 'phosphor-svelte/lib/MagnifyingGlassIcon';
	import SortAscendingIcon from 'phosphor-svelte/lib/SortAscendingIcon';

	let { sourceId = '', repo = '', group = '' }: { sourceId?: string; repo?: string; group?: string } = $props();

	let query = $state('');
	let kind = $state<ModelKind | undefined>(undefined);
	let runtimeId = $state<string | undefined>(undefined);
	let formatId = $state<string | undefined>(undefined);
	let sort = $state<ModelSort>(ModelSort.LAST_USED);
	let runtimes = $state<RuntimeStatus[]>([]);
	let formats = $state<Format[]>([]);
	let tab = $state('about');
	let filtersOpen = $state(false);
	let collect = $state(true);
	let exporting = $state<StoredModel | undefined>(undefined);
	let exportOpen = $state(false);
	let exportDir = $state('');
	let exportBusy = $state(false);
	let fetched = $state<StoredModel | undefined>(undefined);
	let fetchError = $state('');
	let cardWidth = $state(0);
	const folded = $derived(cardWidth > 0 && cardWidth < 1000);

	const list = new ListQuery<StoredModel, { query: string; kind?: ModelKind; runtimeId?: string; formatId?: string; sort: ModelSort }>(
		async (p, cursor, limit, signal) => {
			const res = await rpc.store.listModels({ query: p.query, kind: p.kind ?? ModelKind.UNSPECIFIED, runtimeId: p.runtimeId ?? '', formatId: p.formatId ?? '', sort: p.sort, cursor, limit }, { signal });
			return { items: res.models, next: res.nextCursor };
		},
		[EventKind.MODEL, EventKind.STORE, EventKind.INSTANCE]
	);

	$effect(() => {
		void list.load({ query, kind, runtimeId, formatId, sort });
	});

	onMount(async () => {
		try {
			const [r, f] = await Promise.all([rpc.runtimes.listRuntimes({}), rpc.runtimes.listFormats({})]);
			runtimes = r.runtimes;
			formats = f.formats;
		} catch (err) {
			toast.error('Runtimes failed to list', reason(err));
		}
	});

	const panelOpen = $derived(repo !== '');
	const key = $derived(modelKey({ sourceId, repo, group }));

	// The open model as GetModel answered it until the stream holds it
	$effect(() => {
		const ref = { sourceId, repo, group };
		fetched = undefined;
		fetchError = '';
		if (!ref.repo) return;
		const ac = new AbortController();
		rpc.store
			.getModel(ref, { signal: ac.signal })
			.then((res) => (fetched = res.model))
			.catch((err) => {
				if (!ac.signal.aborted) fetchError = reason(err);
			});
		return () => ac.abort();
	});

	const selected = $derived(panelOpen ? (live.ready ? live.models.get(key) : fetched) : undefined);
	const activeFilters = $derived([kind, runtimeId, formatId].filter((v) => v !== undefined).length);
	const filtering = $derived(query !== '' || activeFilters > 0);
	const searchable = $derived(filtering || !live.ready || live.models.size > 0);
	const kinds = enumOptions(ModelKindSchema).map((o) => ({ value: o.value as ModelKind, label: o.label }));
	const runtimeOptions = $derived(runtimes.map((r) => ({ value: r.runtime?.id ?? '', label: r.runtime?.name || r.runtime?.id || '' })));
	const formatOptions = $derived(formats.map((f) => ({ value: f.id, label: f.id, description: f.description })));
	const sorts = [
		{ value: ModelSort.LAST_USED, label: 'Last used' },
		{ value: ModelSort.PULLED, label: 'Pulled' },
		{ value: ModelSort.SIZE, label: 'Size' },
		{ value: ModelSort.NAME, label: 'Name' }
	];

	function runs(m: StoredModel): boolean {
		return [...live.instances.values()].some((i) => !terminal(i.state) && sameModel(i, m));
	}

	function phrase(m: StoredModel, all = false): string {
		const names = runtimeNames(m.runtimes, runtimes);
		const shown = all || names.length <= 3 ? names : [...names.slice(0, 3), `+${names.length - 3}`];
		return [paramCount(m.descriptor?.parameterCount), kindLabel(m.descriptor?.kind ?? ModelKind.UNSPECIFIED), ...shown].filter(Boolean).join(' · ');
	}

	function precisionTip(m: StoredModel): string {
		const p = m.descriptor?.precision;
		return [m.formatId ? `format ${m.formatId}` : '', p ? `quality ${quality(p.level)}` : ''].filter(Boolean).join('\n');
	}

	function open(m: StoredModel, t = 'about') {
		tab = t;
		void goto(modelPath(m));
	}

	function startExport(m: StoredModel) {
		exporting = m;
		exportOpen = true;
	}

	async function submitExport() {
		if (!exporting) return;
		exportBusy = true;
		if (await exportModel(exporting, exportDir)) exportOpen = false;
		exportBusy = false;
	}

	async function remove(m: StoredModel) {
		if ((await removeModel(m, collect)) && selected && sameModel(selected, m)) void goto('/models');
	}

	function clearFilters() {
		query = '';
		kind = undefined;
		runtimeId = undefined;
		formatId = undefined;
	}
</script>

{#snippet collectSwitch()}
	<Switch bind:checked={collect} label="Collect blobs" />
{/snippet}

{#snippet sortIcon()}<SortAscendingIcon size={16} />{/snippet}

{#snippet kindSelect()}
	<Select bind:value={kind} options={kinds} label="Kind" compact clearable />
{/snippet}

{#snippet runtimeSelect()}
	<Select bind:value={runtimeId} options={runtimeOptions} label="Runtime" compact clearable search />
{/snippet}

{#snippet formatSelect()}
	<Select bind:value={formatId} options={formatOptions} label="Format" compact clearable />
{/snippet}

{#snippet tools()}
	{#if searchable}
		<span class="search"><SearchBox bind:value={query} width="100%" /></span>
		{#if folded}
			<NeoButton elevation={2} aria-expanded={filtersOpen} onclick={() => (filtersOpen = !filtersOpen)}>{#snippet icon()}<FunnelIcon size={20} />{/snippet}Filters{#if activeFilters}&nbsp;{activeFilters}{/if}</NeoButton>
		{:else}
			<span class="filter">{@render kindSelect()}</span>
			<span class="filter">{@render runtimeSelect()}</span>
			<span class="filter">{@render formatSelect()}</span>
		{/if}
		<span class="sort"><Select bind:value={sort} options={sorts} icon={sortIcon} width={selectWidth(sorts.map((s) => s.label))} /></span>
	{/if}
{/snippet}

{#snippet storeHead()}
	<StoreCard />
{/snippet}

{#snippet listView()}
	<div class="frame" bind:clientWidth={cardWidth}>
		<Card title="Models" flush tools={searchable ? tools : undefined}>
			{#if folded && filtersOpen && searchable}
				<div class="filters">
					{@render kindSelect()}
					{@render runtimeSelect()}
					{@render formatSelect()}
				</div>
			{/if}
			{#if !list.settled}
				<Skeleton rows={4} primary="40ch" secondary="28ch" />
			{:else if list.error}
				<Empty icon={CubeIcon} text="Models failed to list" hint={list.error} action="Retry" onclick={() => list.refresh()} />
			{:else if list.items.length === 0 && filtering}
				<Empty icon={MagnifyingGlassIcon} text="No models match" action="Clear filters" onclick={clearFilters} />
			{:else if list.items.length === 0}
				<Empty icon={CubeIcon} text="No models stored" hint="Pulls land here" action="Discover" href="/discover" primary />
			{:else}
				<List head={{ primary: 'Model', secondary: 'Details', facts: ['Size'] }}>
					{#each list.items as m (modelKeyOf(m))}
						{@const running = runs(m)}
						<ListRow
							lamp={running ? 'success' : 'muted'}
							shape={running ? 'filled' : 'hollow'}
							primary={m.repo}
							primaryWidth="40ch"
							primaryTip={m.group ? `${m.repo}\n${m.group}` : m.repo}
							secondary={phrase(m)}
							secondaryTip={phrase(m, true)}
							secondaryWidth="28ch"
							facts={[{ value: bytes(m.bytes) }]}
							selected={selected !== undefined && sameModel(selected, m)}
							onclick={() => open(m)}
							menu={[
								{ label: 'Inspect', onclick: () => open(m, 'fit') },
								{ label: 'Verify', onclick: () => verifyModel(m) },
								{ label: 'Export', onclick: () => startExport(m) },
								{ label: 'Remove', onclick: () => remove(m), confirm: `Remove ${m.repo}?`, confirmBody: collectSwitch, disabled: running, description: running ? 'running' : undefined, danger: true, divider: true }
							]}
						>
							{#snippet badges()}
								{#if m.descriptor?.precision?.label || m.formatId}
									<Tip text={precisionTip(m)} tag="span">
										<StatePill label={m.descriptor?.precision?.label || m.formatId} />
									</Tip>
								{/if}
							{/snippet}
							{#snippet action()}
								<RowAction href={launchHref(m)} label="Launch" />
							{/snippet}
						</ListRow>
					{/each}
				</List>
				<LoadMore next={list.next} loading={list.loading} onmore={() => list.more()} />
			{/if}
		</Card>
	</div>
{/snippet}

<PageHeader tabs={modelsTabs} active="library" info="Models pulled onto this host, ready to launch or inspect for fit" />

<Sheet open={panelOpen} onclose={() => goto('/models')} head={storeHead} list={listView}>
	{#if selected}
		{#key key}
			<ModelPanel model={selected} {runtimes} bind:tab onclose={() => goto('/models')} onexport={startExport} onremoved={() => goto('/models')} />
		{/key}
	{:else}
		<PanelHeader title={repo} kind="repo" subtitle={group || undefined} onclose={() => goto('/models')} />
		{#if fetchError}
			<Empty icon={CubeIcon} text="Model failed to load" hint={fetchError} action="Back" href="/models" />
		{:else if !live.ready}
			<Skeleton rows={6} kind="facts" />
		{:else}
			<Empty icon={CubeIcon} text="No such model" action="Back" href="/models" />
		{/if}
	{/if}
</Sheet>

<Dialog bind:open={exportOpen} title="Export" confirm="Export" busy={exportBusy} disabled={!exportDir} onconfirm={submitExport}>
	{#if exporting}<Str value={exporting.repo} kind="repo" color="muted" width="100%" />{/if}
	<Path bind:value={exportDir} label="Directory" filter="directories" />
</Dialog>

<style>
	.frame {
		min-width: 0;
	}
	.search {
		display: inline-flex;
		width: 16rem;
		max-width: 100%;
	}
	.filter {
		display: inline-flex;
		width: 10rem;
	}
	.sort {
		display: inline-flex;
	}
	.filters {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(14rem, 1fr));
		gap: var(--nebu-space-3) var(--nebu-space-4);
		padding: 0 var(--nebu-card-pad) var(--nebu-space-4);
	}
</style>
