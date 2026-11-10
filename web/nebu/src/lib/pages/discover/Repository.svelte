<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { Model } from '#lib/proto/nebu/v1/model_pb.js';
	import type { ModelCard, Revision, SourceStatus } from '#lib/proto/nebu/v1/source_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Markdown from '#lib/ui/Markdown.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import type { Resolve } from '#lib/ui/markdown.js';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import FileTree, { type TreeItem } from '#lib/ui/FileTree.svelte';
	import PullButton from '#lib/pages/discover/PullButton.svelte';
	import WeightsTable from '#lib/pages/discover/WeightsTable.svelte';
	import { lastSearch } from '#lib/pages/discover/discover.js';
	import CubeIcon from 'phosphor-svelte/lib/CubeIcon';
	import FileTextIcon from 'phosphor-svelte/lib/FileTextIcon';
	import PlugsIcon from 'phosphor-svelte/lib/PlugsIcon';

	let { sourceId, repo }: { sourceId: string; repo: string } = $props();

	const back = lastSearch.href;

	let source = $state<SourceStatus | undefined>(undefined);
	let revisions = $state<Revision[]>([]);
	let revision = $state('');
	let headLoaded = $state(false);
	let headError = $state('');
	let attempt = $state(0);
	let model = $state<Model | undefined>(undefined);
	let resolveError = $state('');
	let card = $state<ModelCard | undefined>(undefined);
	let cardLoading = $state(true);
	let cardError = $state('');
	let tab = $state(page.url.searchParams.get('tab') || 'weights');
	let nodeId = $state<string | undefined>(undefined);
	let width = $state(0);

	const caps = $derived(source?.capabilities);
	const webUrl = $derived(card?.url || (caps?.webUrl ? `${caps.webUrl.replace(/\/$/, '')}/${repo}` : ''));
	const revisionOptions = $derived(revisions.map((r) => ({ value: r.name, label: r.name, tip: [r.detail, r.sizeBytes ? bytes(r.sizeBytes) : '', r.precision].filter(Boolean).join(' · ') || undefined })));
	const showCard = $derived((caps?.card ?? false) && (cardLoading || cardError !== '' || (card !== undefined && (card.html !== '' || card.markdown !== ''))));
	// Wide pages hold the card beside the operator panel, which then stays put while the card scrolls, narrow ones give it a tab
	const beside = $derived(showCard && width >= 1180);
	const tabs = $derived([{ id: 'weights', label: 'Weights' }, { id: 'files', label: 'Files' }, ...(showCard && !beside ? [{ id: 'card', label: 'Card' }] : [])]);
	const activeTab = $derived(tabs.some((t) => t.id === tab) ? tab : 'weights');
	// This node is the picker's resting value, so pulls name no node until another is chosen
	const selfId = $derived([...live.nodes.values()].find((n) => n.self)?.id);
	const pickedNode = $derived(nodeId ?? selfId);
	const storedPaths = $derived(new Set([...live.models.values()].filter((m) => m.sourceId === sourceId && m.repo === repo).flatMap((m) => m.artifacts.map((a) => a.artifact?.path ?? ''))));
	// Relative paths resolve to their download in the repository, anything else against its page
	const resolver = $derived.by((): Resolve => {
		const base = webUrl ? `${webUrl.replace(/\/$/, '')}/` : location.href;
		const file = (p: string) => model?.artifacts.find((a) => a.path === p.replace(/^\.?\//, '').split(/[?#]/)[0])?.url;
		const pageOf = (p: string) => {
			try {
				return new URL(p, base).href;
			} catch {
				return p;
			}
		};
		return { asset: (p) => file(p) ?? pageOf(p), link: (p) => file(p) ?? pageOf(p) };
	});

	async function loadHead() {
		headError = '';
		headLoaded = false;
		try {
			const s = await rpc.sources.listSources({ query: sourceId });
			source = s.sources.find((x) => x.source?.id === sourceId);
			if (source?.capabilities?.revisions) {
				revisions = (await rpc.sources.listRevisions({ sourceId, repo })).revisions;
				revision = revisions.find((r) => r.default)?.name ?? revisions[0]?.name ?? '';
			}
		} catch (err) {
			headError = reason(err);
			toast.error('Repository failed to load', headError);
		} finally {
			headLoaded = true;
		}
	}

	onMount(loadHead);

	$effect(() => {
		void attempt;
		if (!headLoaded || !source) return;
		const rev = revision;
		const withCard = source.capabilities?.card ?? false;
		const ac = new AbortController();
		model = undefined;
		resolveError = '';
		card = undefined;
		cardError = '';
		cardLoading = withCard;
		rpc.sources
			.resolve({ sourceId, repo, revision: rev }, { signal: ac.signal })
			.then((res) => (model = res.model))
			.catch((err) => {
				if (!ac.signal.aborted) resolveError = reason(err);
			});
		if (withCard) {
			rpc.sources
				.getModelCard({ sourceId, repo, revision: rev }, { signal: ac.signal })
				.then((res) => (card = res.card))
				.catch((err) => {
					if (!ac.signal.aborted) cardError = reason(err);
				})
				.finally(() => {
					if (!ac.signal.aborted) cardLoading = false;
				});
		}
		return () => ac.abort();
	});

	async function fetchNodes(query: string, cursor: string, limit: number, signal: AbortSignal) {
		const res = await rpc.mesh.listNodes({ query, cursor, limit }, { signal });
		return { items: res.nodes.map((n) => ({ value: n.id, label: n.name, description: n.self ? 'this node' : n.address })), next: res.nextCursor };
	}

	// Opens a tab and keeps it in the URL
	function openTab(id: string) {
		tab = id;
		const url = new URL(location.href);
		url.searchParams.set('tab', id);
		void goto(url, { replace: true, shallow: true });
	}
</script>

{#snippet modelCard()}
	<Card>
		{#if cardError}
			<Empty icon={FileTextIcon} text="Model card failed to load" hint={cardError} />
		{:else if cardLoading || !card}
			<Skeleton rows={10} kind="text" />
		{:else}
			<Markdown source={card.markdown} html={card.html} resolve={resolver} />
		{/if}
	</Card>
{/snippet}


{#snippet filePull(item: TreeItem)}
	<PullButton {sourceId} {repo} {revision} paths={item.files.map((f) => f.path)} name={item.name} nodeId={nodeId ?? ''} />
{/snippet}

{#snippet warnings()}
	{#if model?.warnings.length}
		<List head={{ primary: 'Warnings' }}>
			{#each model.warnings as w (w)}
				<ListRow lamp="warning" primary={w} primaryKind="message" />
			{/each}
		</List>
	{/if}
{/snippet}

<PageHeader title={repo} back={{ label: 'Discover', href: back }} origin={webUrl && source ? { label: source.source?.name || sourceId, href: webUrl } : undefined} />

<div class="frame" bind:clientWidth={width}>
	{#if headError}
		<Card><Empty icon={PlugsIcon} text="Repository failed to load" hint={headError} action="Retry" onclick={loadHead} /></Card>
	{:else if !headLoaded}
		<Card><Skeleton rows={8} kind="text" /></Card>
	{:else if !source}
		<Card><Empty icon={PlugsIcon} text="No such source" action="Discover" href={back} /></Card>
	{:else}
		<div class="layout" class:beside>
			<aside class="operator" aria-label="Repository">
				<!-- The tabs, then the node the pulls land on and the revision the tables read from -->
				<div class="opbar">
					<div class="tabs"><PageTabs {tabs} active={activeTab} onchange={openTab} /></div>
					{#if live.inMesh}
						<span class="node"><Picker bind:value={() => pickedNode, (v) => (nodeId = v === selfId ? undefined : v)} fetch={fetchNodes} label="Node" compact current={pickedNode ? live.nodes.get(pickedNode)?.name : undefined} /></span>
					{/if}
					{#if revisions.length}
						<Select bind:value={revision} options={revisionOptions} label="Revision" compact fit />
					{/if}
				</div>
				{#if activeTab === 'card'}
					{@render modelCard()}
				{:else}
				<Card flush scroll={beside}>
					{#if resolveError}
						<Empty icon={CubeIcon} text="Repository failed to resolve" hint={resolveError} action="Retry" onclick={() => attempt++} />
					{:else if !model}
						<Skeleton rows={6} dot={false} leading="20px" primary="24ch" secondary="10ch" facts={1} action={false} menu={false} />
					{:else if activeTab === 'weights'}
						<WeightsTable {sourceId} {repo} {revision} {model} nodeId={nodeId ?? ''} />
						{@render warnings()}
					{:else}
						<FileTree artifacts={model.artifacts} stored={storedPaths} action={filePull} />
						{@render warnings()}
					{/if}
				</Card>
				{/if}
			</aside>
			{#if beside}
				<div class="reading">{@render modelCard()}</div>
			{/if}
		</div>
	{/if}
</div>

<style>
	.frame {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-5);
		min-width: 0;
	}
	.layout {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-5);
		min-width: 0;
	}
	.layout.beside {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
		align-items: start;
	}
	.operator {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-3);
		min-width: 0;
	}
	.beside .operator {
		position: sticky;
		top: calc(var(--nebu-top, 61px) + var(--nebu-pad-top));
		max-height: calc(100dvh - var(--nebu-top, 61px) - var(--nebu-pad-top) - var(--nebu-pad-bottom));
	}
	.beside .operator > :global(.card) {
		flex: 1 1 auto;
		min-height: 0;
	}
	.opbar {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: center;
		min-width: 0;
	}
	.tabs {
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
	}
	.node {
		display: inline-flex;
		width: 10rem;
		min-width: 0;
	}
	.reading {
		min-width: 0;
	}
</style>
