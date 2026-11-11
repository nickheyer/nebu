<script lang="ts">
	import { goto } from '$app/navigation';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes } from '#lib/format.js';
	import { live, modelKey } from '#lib/live/events.svelte.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { ModelSort, type StoredModel } from '#lib/proto/nebu/v1/store_pb.js';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import OriginLink from '#lib/ui/OriginLink.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import SearchBox from '#lib/ui/SearchBox.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import { repoPath } from '#lib/pages/discover/discover.js';

	import ModelPanel from '#lib/pages/models/ModelPanel.svelte';
	import StoreCard from '#lib/pages/models/StoreCard.svelte';
	import { Contexts } from '#lib/pages/models/context.svelte.js';
	import { exportModel, launchHref, modelKeyOf, modelPath, removeModel, sameModel, verifyModel } from '#lib/pages/models/models.js';
	import { modelsTabs } from '#lib/pages/models/tabs.js';
	import { terminal } from '#lib/pages/serve/serve.js';
	import CubeIcon from 'phosphor-svelte/lib/CubeIcon';
	import MagnifyingGlassIcon from 'phosphor-svelte/lib/MagnifyingGlassIcon';
	import SortAscendingIcon from 'phosphor-svelte/lib/SortAscendingIcon';

	let { sourceId = '', repo = '', group = '' }: { sourceId?: string; repo?: string; group?: string } = $props();

	type Repo = { sourceId: string; repo: string; models: StoredModel[] };

	let query = $state('');
	let sort = $state<ModelSort>(ModelSort.LAST_USED);
	let tab = $state('about');
	let collect = $state(true);
	let exporting = $state<StoredModel | undefined>(undefined);
	let exportOpen = $state(false);
	let exportDir = $state('');
	let exportBusy = $state(false);
	let fetched = $state<StoredModel | undefined>(undefined);
	let fetchError = $state('');
	const contexts = new Contexts();

	const list = new ListQuery<StoredModel, { query: string; sort: ModelSort }>(
		async (p, cursor, limit, signal) => {
			const res = await rpc.store.listModels({ query: p.query, sort: p.sort, byRepo: true, cursor, limit }, { signal });
			return { items: res.models, next: res.nextCursor };
		},
		[EventKind.MODEL, EventKind.STORE, EventKind.INSTANCE]
	);

	$effect(() => {
		void list.load({ query, sort });
	});

	// The listed models under the repository each was pulled from, in the order the daemon answers
	const repos = $derived.by(() => {
		const out: Repo[] = [];
		for (const m of list.items) {
			const last = out[out.length - 1];
			if (last && last.sourceId === m.sourceId && last.repo === m.repo) last.models.push(m);
			else out.push({ sourceId: m.sourceId, repo: m.repo, models: [m] });
		}
		return out;
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
	const searchable = $derived(query !== '' || !live.ready || live.models.size > 0);
	const sorts = [
		{ value: ModelSort.LAST_USED, label: 'Last used' },
		{ value: ModelSort.PULLED, label: 'Pulled' },
		{ value: ModelSort.SIZE, label: 'Size' },
		{ value: ModelSort.NAME, label: 'Name' }
	];

	function runs(m: StoredModel): boolean {
		return [...live.instances.values()].some((i) => !terminal(i.state) && sameModel(i, m));
	}

	function open(m: StoredModel) {
		tab = 'about';
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

</script>

{#snippet collectSwitch()}
	<Switch bind:checked={collect} label="Delete its files" info="Files another model uses stay" compact />
{/snippet}

{#snippet sortIcon()}<SortAscendingIcon size={16} />{/snippet}

{#snippet tools()}
	<span class="search"><SearchBox bind:value={query} width="100%" /></span>
	<span class="sort"><Select bind:value={sort} options={sorts} icon={sortIcon} fit /></span>
{/snippet}

{#snippet storeHead()}
	<StoreCard />
{/snippet}

{#snippet modelRow(m: StoredModel)}
	{@const running = runs(m)}
	<ListRow
		lamp={running ? 'success' : 'muted'}
		shape={running ? 'filled' : 'hollow'}
		primary={m.group || m.formatId}
		facts={[{ value: bytes(m.bytes) }]}
		selected={selected !== undefined && sameModel(selected, m)}
		onclick={() => open(m)}
		menu={[
			{ label: 'Verify', onclick: () => verifyModel(m) },
			{ label: 'Export', onclick: () => startExport(m) },
			{ label: 'Remove', onclick: () => remove(m), confirm: `Remove ${m.repo} ${m.group}?`, confirmBody: collectSwitch, disabled: running, description: running ? 'running' : undefined, danger: true, divider: true }
		]}
	>
		{#snippet action()}
			{#if m.runtimes}<RowAction href={launchHref(m)} label="Launch" play />{/if}
		{/snippet}
	</ListRow>
{/snippet}

{#snippet listView()}
	<Card title="Models" flush tools={searchable ? tools : undefined}>
		{#if !list.settled}
			<Skeleton rows={4} primary="28ch" secondary={false} />
		{:else if list.error}
			<Empty icon={CubeIcon} text="Models failed to list" hint={list.error} action="Retry" onclick={() => list.refresh()} />
		{:else if list.items.length === 0 && query}
			<Empty icon={MagnifyingGlassIcon} text="No models match" action="Clear search" onclick={() => (query = '')} />
		{:else if list.items.length === 0}
			<Empty icon={CubeIcon} text="No models stored" action="Discover" href="/discover" primary />
		{:else}
			<List>
				{#each repos as r (`${r.sourceId}\n${r.repo}`)}
					<div class="repo">
						<OriginLink href={repoPath(r.sourceId, r.repo)}><Str value={r.repo} kind="repo" weight={500} /></OriginLink>
					</div>
					{#each r.models as m (modelKeyOf(m))}{@render modelRow(m)}{/each}
				{/each}
			</List>
			<LoadMore next={list.next} loading={list.loading} onmore={() => list.more()} />
		{/if}
	</Card>
{/snippet}

<PageHeader tabs={modelsTabs} active="library" />

<Sheet open={panelOpen} onclose={() => goto('/models')} head={storeHead} list={listView}>
	{#if selected}
		{#key key}
			<ModelPanel model={selected} {contexts} bind:tab onclose={() => goto('/models')} onexport={startExport} onremoved={() => goto('/models')} />
		{/key}
	{:else}
		<PanelHeader title={repo} kind="repo" subtitle={group || undefined} href={repoPath(sourceId, repo)} onclose={() => goto('/models')} />
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
	.search {
		display: inline-flex;
		width: 16rem;
		max-width: 100%;
	}
	.sort {
		display: inline-flex;
	}
	/* A repository heads the models pulled from it, on the band a column head would take */
	.repo {
		display: flex;
		grid-column: 1 / -1;
		align-items: center;
		min-height: 36px;
		padding: var(--nebu-space-1) var(--nebu-row-inset, var(--nebu-space-2));
		background: var(--nebu-surface-sunken);
		border-top: 1px solid var(--nebu-border);
		border-bottom: 1px solid var(--nebu-border);
	}
	/* The last model of a repository leaves its rule to the band that follows */
	:global(.rowwrap:has(+ .repo)) {
		border-bottom-color: transparent;
	}
</style>
