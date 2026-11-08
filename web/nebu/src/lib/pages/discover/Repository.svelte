<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { InspectResponse, Part, PartsResponse } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { ArtifactRole, type Artifact, type Model } from '#lib/proto/nebu/v1/model_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import type { ModelCard, Revision, SourceStatus } from '#lib/proto/nebu/v1/source_pb.js';
	import { TaskState, type Task } from '#lib/proto/nebu/v1/task_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Markdown from '#lib/ui/Markdown.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import type { Resolve } from '#lib/ui/markdown.js';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Columns from '#lib/ui/layout/Columns.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import FitTab from '#lib/pages/models/FitTab.svelte';
	import ShardGrid from '#lib/pages/models/ShardGrid.svelte';
	import { launchHref, roleIcon, selectWidth, shardPattern } from '#lib/pages/models/models.js';
	import { groupFiles, lastSearch, weightGroups } from '#lib/pages/discover/discover.js';
	import ArrowSquareOutIcon from 'phosphor-svelte/lib/ArrowSquareOutIcon';
	import CubeIcon from 'phosphor-svelte/lib/CubeIcon';
	import DownloadIcon from 'phosphor-svelte/lib/DownloadIcon';
	import FileTextIcon from 'phosphor-svelte/lib/FileTextIcon';
	import FilesIcon from 'phosphor-svelte/lib/FilesIcon';
	import PlugsIcon from 'phosphor-svelte/lib/PlugsIcon';

	let { sourceId, repo }: { sourceId: string; repo: string } = $props();

	const back = lastSearch.href;
	const external: Record<string, string> = { target: '_blank', rel: 'noreferrer' };
	const downloadAttrs: Record<string, string> = { download: '' };

	let source = $state<SourceStatus | undefined>(undefined);
	let runtimes = $state<RuntimeStatus[]>([]);
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
	let groupKey = $state('');
	let tab = $state(page.url.searchParams.get('tab') || 'card');
	let inspected = $state<InspectResponse | undefined>(undefined);
	let inspectError = $state('');
	let inspectKey = '';
	let partsRes = $state<PartsResponse | undefined>(undefined);
	let partsError = $state('');
	let partsKey = '';
	let chosen = $state<string[]>([]);
	let alone = $state(false);
	let nodeId = $state<string | undefined>(undefined);
	let pulling = $state(false);
	let free = $state(false);
	let width = $state(0);

	const caps = $derived(source?.capabilities);
	const webUrl = $derived(card?.url || (caps?.webUrl ? `${caps.webUrl.replace(/\/$/, '')}/${repo}` : ''));
	const groups = $derived(weightGroups(model));
	const groupOf = (g: { formatId: string; name: string }) => `${g.formatId}\n${g.name}`;
	const chosenGroup = $derived(groups.find((g) => groupOf(g) === groupKey));
	const group = $derived(chosenGroup?.name ?? '');
	const groupOptions = $derived(groups.map((g) => ({ value: groupOf(g), label: g.name || g.formatId, description: `${g.formatId} · ${bytes(g.bytes)}` })));
	const revisionOptions = $derived(revisions.map((r) => ({ value: r.name, label: r.name, tip: [r.detail, r.sizeBytes ? bytes(r.sizeBytes) : '', r.precision].filter(Boolean).join(' · ') || undefined })));
	const files = $derived(groupFiles(model, group));
	const filesBytes = $derived(files.reduce((n, a) => n + Number(a.sizeBytes), 0));
	const ref = $derived({ sourceId, repo, group });
	const stored = $derived([...live.models.values()].some((m) => m.sourceId === sourceId && m.repo === repo && m.group === group));
	const missing = $derived((partsRes?.parts ?? []).filter((p) => p.required && !p.stored && !p.bundled));
	const allChosen = $derived(missing.every((p) => chosen.includes(p.slot)));
	const partsBytes = $derived(alone ? 0 : allChosen ? Number(partsRes?.pullBytes ?? 0) : missing.filter((p) => chosen.includes(p.slot)).reduce((n, p) => n + Number(p.sizeBytes), 0));
	const showCard = $derived((caps?.card ?? false) && (cardLoading || cardError !== '' || (card !== undefined && (card.html !== '' || card.markdown !== ''))));
	// Wide pages hold the card beside the tabs, which then drop it
	const beside = $derived(showCard && width >= 1500);
	const tabs = $derived([...(showCard && !beside ? [{ id: 'card', label: 'Card' }] : []), { id: 'files', label: 'Files' }, { id: 'fit', label: 'Fit' }, { id: 'pull', label: 'Pull' }]);
	const selfName = $derived([...live.nodes.values()].find((n) => n.self)?.name ?? live.host?.hostname ?? '');
	const activeTab = $derived(tabs.some((t) => t.id === tab) ? tab : 'files');
	const weights = $derived(files.filter((a) => a.role === ArtifactRole.WEIGHTS));
	const supporting = $derived(files.filter((a) => a.role !== ArtifactRole.WEIGHTS));
	const weightsBytes = $derived(weights.reduce((n, a) => n + Number(a.sizeBytes), 0));
	const storedAny = $derived([...live.models.values()].some((m) => m.sourceId === sourceId && m.repo === repo));
	const revisionWidth = $derived(selectWidth(revisions.map((r) => r.name)));
	const groupWidth = $derived(selectWidth(groupOptions.map((o) => o.label)));
	// Relative paths resolve to their download in the repository, anything else against its page
	const resolver = $derived.by((): Resolve => {
		const base = webUrl ? `${webUrl.replace(/\/$/, '')}/` : location.href;
		const file = (p: string) => model?.artifacts.find((a) => a.path === p.replace(/^\.?\//, '').split(/[?#]/)[0])?.url;
		const page = (p: string) => {
			try {
				return new URL(p, base).href;
			} catch {
				return p;
			}
		};
		return { asset: (p) => file(p) ?? page(p), link: (p) => file(p) ?? page(p) };
	});

	async function loadHead() {
		headError = '';
		headLoaded = false;
		try {
			const [s, rt] = await Promise.all([rpc.sources.listSources({ query: sourceId }), rpc.runtimes.listRuntimes({})]);
			source = s.sources.find((x) => x.source?.id === sourceId);
			runtimes = rt.runtimes;
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
			.then((res) => {
				model = res.model;
				const first = weightGroups(res.model)[0];
				groupKey = first ? groupOf(first) : '';
			})
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

	$effect(() => {
		if (!model || !group) return;
		const key = `${revision}\n${group}`;
		if (key === inspectKey) return;
		inspectKey = key;
		inspected = undefined;
		inspectError = '';
		rpc.estimate
			.inspect({ sourceId, repo, revision, groups: [group] })
			.then((res) => {
				if (inspectKey === key) inspected = res;
			})
			.catch((err) => {
				if (inspectKey === key) inspectError = reason(err);
			});
	});

	// The parts the chosen group needs, asked for once the pull tab opens
	$effect(() => {
		if (activeTab !== 'pull' || !model) return;
		const key = `${revision}\n${group}`;
		if (key === partsKey) return;
		partsKey = key;
		partsRes = undefined;
		partsError = '';
		rpc.estimate
			.parts({ sourceId, repo, revision, group })
			.then((res) => {
				if (partsKey !== key) return;
				partsRes = res;
				chosen = res.parts.filter((p) => p.required && !p.stored && !p.bundled && !p.error).map((p) => p.slot);
			})
			.catch((err) => {
				if (partsKey === key) partsError = reason(err);
			});
	});

	async function fetchNodes(query: string, cursor: string, limit: number, signal: AbortSignal) {
		const res = await rpc.mesh.listNodes({ query, cursor, limit }, { signal });
		return { items: res.nodes.map((n) => ({ value: n.id, label: n.name, description: n.self ? 'this node' : n.address })), next: res.nextCursor };
	}

	async function succeeded(task: Task): Promise<boolean> {
		for await (const w of rpc.tasks.watchTask({ id: task.id })) {
			const s = w.task?.state;
			if (s === TaskState.SUCCEEDED) return true;
			if (s === TaskState.FAILED || s === TaskState.CANCELED) return false;
		}
		throw new Error(`${task.title}: watch ended early`);
	}

	// Pulls the group and its chosen parts, launching after when asked
	async function pull(launch: boolean, runtimeId?: string, context?: number) {
		pulling = true;
		const node = nodeId ?? '';
		const own = !alone && allChosen;
		try {
			const started: Task[] = [];
			const main = await rpc.store.pull({ sourceId, repo, revision, group, alone: !own, nodeId: node });
			if (main.task) {
				followTask(main.task, `Pull ${repo}`);
				started.push(main.task);
			}
			if (!alone && !own) {
				for (const p of missing.filter((x: Part) => chosen.includes(x.slot))) {
					const res = await rpc.store.pull({ sourceId: p.sourceId, repo: p.repo, revision: p.revision, group: p.group, alone: true, nodeId: node, paths: p.paths, slot: p.paths.length ? p.slot : '' });
					if (res.task) {
						followTask(res.task, `Pull ${p.label || p.slot}`);
						started.push(res.task);
					}
				}
			}
			if (launch && (await Promise.all(started.map(succeeded))).every(Boolean)) void goto(launchHref(ref, runtimeId, context));
		} catch (err) {
			toast.error('Pull failed', reason(err));
		} finally {
			pulling = false;
		}
	}

	// Opens a tab and keeps it in the URL
	function openTab(id: string) {
		tab = id;
		const url = new URL(location.href);
		url.searchParams.set('tab', id);
		void goto(url, { replace: true, shallow: true });
	}

	function fitLaunch(runtimeId: string, context: number) {
		if (stored) void goto(launchHref(ref, runtimeId, context));
		else openTab('pull');
	}

	function fileTip(a: Artifact): string {
		return [a.path, a.shardCount ? `shard ${a.shardIndex} of ${a.shardCount}` : ''].filter(Boolean).join('\n');
	}
</script>

{#snippet download(a: Artifact)}
	<NeoButton text href={a.url} {...downloadAttrs} aria-label="Download {a.path}">{#snippet icon()}<DownloadIcon size={20} />{/snippet}</NeoButton>
{/snippet}

{#snippet fileRow(a: Artifact)}
	{@const Icon = roleIcon(a.role)}
	{#snippet fileAction()}{@render download(a)}{/snippet}
	<ListRow primary={a.path} primaryKind="path" primaryTip={fileTip(a)} facts={[{ value: bytes(a.sizeBytes) }]} action={a.url ? fileAction : undefined}>
		{#snippet leading()}<span class="icon"><Icon size={20} /></span>{/snippet}
	</ListRow>
{/snippet}

{#snippet groupSelect()}
	{#if groups.length > 1}
		<span class="tool"><Select bind:value={groupKey} options={groupOptions} search={groups.length > 8} width={groupWidth} /></span>
	{/if}
{/snippet}

{#snippet repoTools()}
	{@render groupSelect()}
	{#if revisions.length}
		<span class="tool"><Select bind:value={revision} options={revisionOptions} search={revisions.length > 8} width={revisionWidth} /></span>
	{/if}
	{#if webUrl}
		<NeoButton text href={webUrl} {...external} aria-label={source?.source?.name || sourceId}>{#snippet icon()}<ArrowSquareOutIcon size={20} />{/snippet}</NeoButton>
	{/if}
{/snippet}

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

{#snippet resolving(title: string)}
	<Card {title}>
		{#if resolveError}
			<Empty icon={CubeIcon} text="Repository failed to resolve" hint={resolveError} action="Retry" onclick={() => attempt++} />
		{:else}
			<Skeleton rows={5} dot={false} leading="20px" primary="40ch" secondary={false} action={false} menu={false} />
		{/if}
	</Card>
{/snippet}

{#snippet filesCard()}
	{#if !model}
		{@render resolving('Files')}
	{:else}
		<Card title="Files" flush>
			{#snippet tools()}
				{#if storedAny}<StatePill label="Stored" lamp="success" />{/if}
				<span class="total">{bytes(filesBytes)}</span>
				{@render repoTools()}
			{/snippet}
			{#if files.length === 0}
				<Empty icon={FilesIcon} text="No weights" />
			{:else}
				<List head={{ primary: 'File', facts: ['Size'] }}>
					{#if weights.length > 1}
						{#snippet shards()}
							<ShardGrid shards={weights.map((a) => ({ path: a.path, index: a.shardIndex, bytes: a.sizeBytes, url: a.url || undefined }))} />
						{/snippet}
						<ListRow primary={shardPattern(weights.map((a) => a.path))} primaryKind="path" facts={[{ value: bytes(weightsBytes) }]} expand={shards}>
							{#snippet leading()}<span class="icon"><CubeIcon size={20} /></span>{/snippet}
						</ListRow>
					{:else}
						{#each weights as a (a.path)}{@render fileRow(a)}{/each}
					{/if}
					{#each supporting as a (a.path)}{@render fileRow(a)}{/each}
				</List>
			{/if}
		</Card>
		{#if model.warnings.length}
			<Card title="Warnings" flush>
				<List>
					{#each model.warnings as w (w)}
						<ListRow lamp="warning" primary={w} primaryKind="message" />
					{/each}
				</List>
			</Card>
		{/if}
	{/if}
{/snippet}

{#snippet fitCard()}
	{#if !model}
		{@render resolving('Fit')}
	{:else}
		<Card title="Fit" flush>
			{#snippet tools()}
				<Switch bind:checked={free} label="Against free memory" />
				{@render repoTools()}
			{/snippet}
			<FitTab {group} result={inspected} error={inspectError} {runtimes} {free} launchLabel={stored ? 'Launch' : 'Pull'} onlaunch={fitLaunch} />
		</Card>
	{/if}
{/snippet}

{#snippet pullCard()}
	{#if !model}
		{@render resolving('Pull')}
	{:else}
		<Card title="Pull">
			{#snippet tools()}{@render repoTools()}{/snippet}
			{#snippet footer()}
				<Tip facts={[{ label: 'group', value: bytes(filesBytes) }, { label: 'parts', value: bytes(partsBytes) }]} tag="span">
					<span class="sum"><span class="secondary">Total</span><span class="num">{bytes(filesBytes + partsBytes)}</span></span>
				</Tip>
				<span class="fill"></span>
				{#if stored}<NeoButton text href={launchHref(ref)}>Launch</NeoButton>{/if}
				<NeoButton elevation={2} loading={pulling} onclick={() => pull(false)}>Pull</NeoButton>
				<NeoButton class="primary" elevation={2} loading={pulling} onclick={() => pull(true)}>Pull and launch</NeoButton>
			{/snippet}
			{#if live.inMesh}
				<Picker bind:value={nodeId} fetch={fetchNodes} label="Node" placeholder={selfName} clearable current={nodeId ? live.nodes.get(nodeId)?.name : undefined} />
			{/if}
			<Switch bind:checked={alone} label="Weights only" />
			{#if partsError}
				<Empty icon={CubeIcon} text="Parts failed to list" hint={partsError} />
			{:else if !partsRes}
				<Skeleton rows={2} dot={false} leading="16px" primary="24ch" secondary="20ch" facts={0} action={false} menu={false} />
			{:else if missing.length}
				<Checklist
					bind:value={chosen}
					options={missing.map((p) => ({ value: p.slot, label: p.label || p.slot, description: [p.name, p.sizeBytes ? bytes(p.sizeBytes) : ''].filter(Boolean).join(' · '), disabled: p.error !== '', tip: p.error || [p.repo, p.group].filter(Boolean).join(' ') || undefined }))}
					label="Parts"
					disabled={alone}
				/>
			{/if}
		</Card>
	{/if}
{/snippet}

{#snippet tabCards()}
	{#if activeTab === 'files'}
		{@render filesCard()}
	{:else if activeTab === 'fit'}
		{@render fitCard()}
	{:else}
		{@render pullCard()}
	{/if}
{/snippet}

<PageHeader title={repo} back={{ label: 'Discover', href: back }} tabs={source ? tabs : undefined} active={activeTab} ontab={openTab} info="One repository at a source, its card, files, fit on this host and the pull that stores it" />

<div class="frame" bind:clientWidth={width}>
	{#if headError}
		<Card><Empty icon={PlugsIcon} text="Repository failed to load" hint={headError} action="Retry" onclick={loadHead} /></Card>
	{:else if !headLoaded}
		<Card><Skeleton rows={8} kind="text" /></Card>
	{:else if !source}
		<Card><Empty icon={PlugsIcon} text="No such source" action="Discover" href={back} /></Card>
	{:else if beside}
		<Columns ratio="1/1">
			{#snippet left()}{@render modelCard()}{/snippet}
			{#snippet right()}{@render tabCards()}{/snippet}
		</Columns>
	{:else if activeTab === 'card'}
		{@render modelCard()}
	{:else}
		{@render tabCards()}
	{/if}
</div>

<style>
	.frame {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-5);
		min-width: 0;
	}
	.tool {
		display: inline-flex;
	}
	.icon {
		display: inline-flex;
		color: var(--nebu-text-muted);
	}
	.total {
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
	.sum {
		display: inline-flex;
		gap: var(--nebu-space-2);
		align-items: baseline;
	}
</style>
