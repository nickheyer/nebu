<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, enumOptions } from '#lib/format.js';
	import { live, modelKey } from '#lib/live/events.svelte.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { ModelKind, ModelKindSchema, type Format } from '#lib/proto/nebu/v1/model_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { ModelSort, type StoredModel } from '#lib/proto/nebu/v1/store_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import SearchBox from '#lib/ui/SearchBox.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import ModelPanel from '#lib/pages/models/ModelPanel.svelte';
	import StoreBar from '#lib/pages/models/StoreBar.svelte';
	import { exportModel, kindLabel, launchHref, modelKeyOf, modelPath, paramCount, quality, removeModel, runtimeNames, sameModel, verifyModel } from '#lib/pages/models/models.js';
	import { terminal } from '#lib/pages/serve/serve.js';
	import FunnelIcon from 'phosphor-svelte/lib/FunnelIcon';

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

	// The open model as GetModel answered it, the stream's copy taking over once the snapshot is in
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

	// Parameter count, kind and up to three compatible runtimes with the rest counted
	function phrase(m: StoredModel, all = false): string {
		const names = runtimeNames(m.runtimes, runtimes);
		const shown = all || names.length <= 3 ? names : [...names.slice(0, 3), `+${names.length - 3}`];
		return [paramCount(m.descriptor?.parameterCount), kindLabel(m.descriptor?.kind ?? ModelKind.UNSPECIFIED), ...shown].filter(Boolean).join(' · ');
	}

	function precisionTip(m: StoredModel): string {
		const p = m.descriptor?.precision;
		return [`format ${m.formatId || 'none'}`, p ? `quality ${quality(p.level)}` : ''].filter(Boolean).join('\n');
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
	<Switch bind:checked={collect} label="Collect unreferenced blobs" />
{/snippet}

{#snippet filterFields()}
	<Select bind:value={kind} options={kinds} label="Kind" placeholder="Any" clearable />
	<Select bind:value={runtimeId} options={runtimeOptions} label="Runtime" placeholder="Any" clearable search />
	<Select bind:value={formatId} options={formatOptions} label="Format" placeholder="Any" clearable />
{/snippet}

{#snippet toolbar()}
	<Row gap={12}>
		<Cell min={viewport.mobile ? '100%' : '14rem'} max={viewport.mobile ? 'none' : '24rem'}>
			<SearchBox bind:value={query} placeholder="Search models" width="100%" />
		</Cell>
		{#if viewport.mobile}
			<Cell min="8rem" max="12rem">
				<NeoButton elevation={2} onclick={() => (filtersOpen = true)}>{#snippet icon()}<FunnelIcon size={20} />{/snippet}Filters{#if activeFilters}&nbsp;{activeFilters}{/if}</NeoButton>
			</Cell>
			<Cell min="9rem" max="12rem" push>
				<Select bind:value={sort} options={sorts} />
			</Cell>
		{:else}
			<Cell min="10rem" max="12rem"><Select bind:value={kind} options={kinds} placeholder="Kind" clearable /></Cell>
			<Cell min="10rem" max="12rem"><Select bind:value={runtimeId} options={runtimeOptions} placeholder="Runtime" clearable search /></Cell>
			<Cell min="10rem" max="12rem"><Select bind:value={formatId} options={formatOptions} placeholder="Format" clearable /></Cell>
			<Cell min="10rem" max="12rem"><Select bind:value={sort} options={sorts} /></Cell>
		{/if}
	</Row>
{/snippet}

{#snippet listView()}
	<Stack gap={16}>
		<StoreBar />
		{@render toolbar()}
		{#if !list.settled}
			<Skeleton rows={4} />
		{:else if list.error}
			<Empty text={list.error} action="Retry" onclick={() => list.refresh()} />
		{:else if list.items.length === 0}
			{#if query || activeFilters}
				<Empty text="No models match" action="Clear filters" onclick={clearFilters} />
			{:else}
				<Empty text="No models stored" action="Discover" href="/discover" />
			{/if}
		{:else}
			<Stack gap={0}>
				{#each list.items as m (modelKeyOf(m))}
					{@const running = runs(m)}
					<ListRow
						lamp={running ? 'success' : 'muted'}
						shape={running ? 'filled' : 'hollow'}
						primary={m.repo}
						primaryKind="repo"
						primaryTip={m.group ? `${m.repo}\n${m.group}` : m.repo}
						secondary={phrase(m)}
						secondaryTip={phrase(m, true)}
						facts={[{ value: bytes(m.bytes) }]}
						selected={selected !== undefined && sameModel(selected, m)}
						onclick={() => open(m)}
						menu={[
							{ label: 'Inspect', onclick: () => open(m, 'fit') },
							{ label: 'Verify', onclick: () => verifyModel(m) },
							{ label: 'Export', onclick: () => startExport(m) },
							{ label: 'Remove', onclick: () => remove(m), confirm: `Remove ${m.repo}`, confirmBody: collectSwitch, disabled: running, description: running ? 'instance running' : undefined, danger: true, divider: true }
						]}
					>
						{#snippet badges()}
							<Tip text={precisionTip(m)} tag="span">
								<StatePill label={m.descriptor?.precision?.label || m.formatId || 'unknown'} />
							</Tip>
						{/snippet}
						{#snippet action()}
							<NeoButton text href={launchHref(m)}>Launch</NeoButton>
						{/snippet}
					</ListRow>
				{/each}
			</Stack>
			<LoadMore next={list.next} loading={list.loading} onmore={() => list.more()} />
		{/if}
	</Stack>
{/snippet}

<Sheet open={panelOpen} onclose={() => goto('/models')} list={listView} empty="Select a model">
	{#if selected}
		{#key key}
			<ModelPanel model={selected} {runtimes} bind:tab onclose={() => goto('/models')} onexport={startExport} onremoved={() => goto('/models')} />
		{/key}
	{:else if fetchError}
		<Empty text={fetchError} action="Back" href="/models" />
	{:else if !live.ready}
		<Skeleton rows={6} height={32} />
	{:else}
		<Empty text="No such model" action="Back" href="/models" />
	{/if}
</Sheet>

<Sheet bind:open={filtersOpen}>
	<PanelHeader title="Filters" onclose={() => (filtersOpen = false)} />
	<Stack gap={12}>{@render filterFields()}</Stack>
</Sheet>

<NeoDialog bind:open={exportOpen} modal rounded="md" filled elevation={2} width="min(32rem, 92vw)" padding="16px">
	<Stack gap={12}>
		<h3>Export</h3>
		{#if exporting}<Str value={exporting.repo} kind="repo" color="muted" />{/if}
		<Path bind:value={exportDir} label="Directory" filter="directories" />
		<Row gap={12} justify="flex-end">
			<NeoButton text onclick={() => (exportOpen = false)}>Cancel</NeoButton>
			<NeoButton class="primary" elevation={2} loading={exportBusy} disabled={!exportDir} onclick={submitExport}>Export</NeoButton>
		</Row>
	</Stack>
</NeoDialog>
