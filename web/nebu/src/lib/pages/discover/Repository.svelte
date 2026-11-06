<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { InspectResponse, Part, PartsResponse } from '#lib/proto/nebu/v1/estimate_pb.js';
	import type { Artifact, Model } from '#lib/proto/nebu/v1/model_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import type { ModelCard, Revision, SourceStatus } from '#lib/proto/nebu/v1/source_pb.js';
	import { TaskState, type Task } from '#lib/proto/nebu/v1/task_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Markdown from '#lib/ui/Markdown.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import type { Resolve } from '#lib/ui/markdown.js';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Split from '#lib/ui/layout/Split.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import FitTab from '#lib/pages/models/FitTab.svelte';
	import { launchHref, roleIcon } from '#lib/pages/models/models.js';
	import { groupFiles, lastSearch, weightGroups } from '#lib/pages/discover/discover.js';
	import ArrowLeftIcon from 'phosphor-svelte/lib/ArrowLeftIcon';
	import ArrowSquareOutIcon from 'phosphor-svelte/lib/ArrowSquareOutIcon';
	import DownloadIcon from 'phosphor-svelte/lib/DownloadIcon';

	let { sourceId, repo }: { sourceId: string; repo: string } = $props();

	const back = lastSearch.href;
	// Anchor attributes the button passes through to the link it renders
	const external: Record<string, string> = { target: '_blank', rel: 'noreferrer' };
	const download: Record<string, string> = { download: '' };

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
	let tab = $state('card');
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
	// Every missing part chosen lets the daemon pull them with the group, its own dependency path
	const allChosen = $derived(missing.every((p) => chosen.includes(p.slot)));
	const partsBytes = $derived(alone ? 0 : allChosen ? Number(partsRes?.pullBytes ?? 0) : missing.filter((p) => chosen.includes(p.slot)).reduce((n, p) => n + Number(p.sizeBytes), 0));
	// The card sits beside the tabs on ultrawide, so the tabs row drops it there
	const tabs = $derived([...(viewport.ultrawide ? [] : [{ id: 'card', label: 'Card' }]), { id: 'files', label: 'Files', count: model ? files.length : undefined }, { id: 'fit', label: 'Fit' }, { id: 'pull', label: 'Pull' }]);
	const activeTab = $derived(viewport.ultrawide && tab === 'card' ? 'files' : tab);
	// Files the card names by path resolve to their download in the repository, anything else against its page
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

	// Resolve and the card follow the chosen revision
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

	// The fit of the chosen group on this host, planned as soon as the group is known
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

	// The parts the chosen group needs, asked for once the pull tab opens, every missing one chosen
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

	// Waits for a task to end, answering whether it succeeded
	async function succeeded(task: Task): Promise<boolean> {
		for await (const w of rpc.tasks.watchTask({ id: task.id })) {
			const s = w.task?.state;
			if (s === TaskState.SUCCEEDED) return true;
			if (s === TaskState.FAILED || s === TaskState.CANCELED) return false;
		}
		throw new Error(`${task.title}: watch ended early`);
	}

	// Pulls the group and its chosen parts, each alone when only some are chosen, launching after when asked
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

	// A fit pill launches a stored group here, else opens the pull tab
	function fitLaunch(runtimeId: string, context: number) {
		if (stored) void goto(launchHref(ref, runtimeId, context));
		else tab = 'pull';
	}

	function fileTip(a: Artifact): string {
		return [a.path, a.shardCount ? `shard ${a.shardIndex} of ${a.shardCount}` : ''].filter(Boolean).join('\n');
	}
</script>

{#snippet header()}
	<Row gap={12} wrap={false}>
		<Cell grow={false}>
			<NeoButton text href={back} aria-label="Back to Discover">{#snippet icon()}<ArrowLeftIcon size={20} />{/snippet}{#if !viewport.mobile}Discover{/if}</NeoButton>
		</Cell>
		<Cell min="12rem">
			<h2 class="title"><Str value={repo} kind="repo" tip={repo} weight={600} width="100%" /></h2>
		</Cell>
		{#if webUrl}
			<Cell grow={false}>
				<NeoButton text href={webUrl} {...external}>{source?.source?.name || sourceId}{#snippet icon()}<ArrowSquareOutIcon size={20} />{/snippet}</NeoButton>
			</Cell>
		{/if}
		{#if revisions.length}
			<Cell min="8rem" max="12rem" grow={false}>
				<Select bind:value={revision} options={revisionOptions} search={revisions.length > 8} />
			</Cell>
		{/if}
	</Row>
{/snippet}

{#snippet cardView()}
	{#if cardError}
		<p class="error-text">{cardError}</p>
	{:else if cardLoading}
		<Skeleton rows={6} height={24} />
	{:else if card && (card.html || card.markdown)}
		<Markdown source={card.markdown} html={card.html} resolve={resolver} />
	{:else}
		<Empty text="No model card" />
	{/if}
{/snippet}

{#snippet filesView()}
	{#if resolveError}
		<Empty text={resolveError} action="Retry" onclick={() => attempt++} />
	{:else if !model}
		<Skeleton rows={5} height={36} />
	{:else}
		<Stack gap={12}>
			{#if groups.length > 1}
				<Row gap={12}>
					<Cell min="12rem" max="20rem"><Select bind:value={groupKey} options={groupOptions} placeholder="Group" /></Cell>
					<Cell grow={false} push><span class="secondary nowrap">{plural(files.length, 'file')}, {bytes(filesBytes)}</span></Cell>
				</Row>
			{:else}
				<span class="secondary">{plural(files.length, 'file')}, {bytes(filesBytes)}</span>
			{/if}
			<Stack gap={0}>
				{#each files as a (a.path)}
					{@const Icon = roleIcon(a.role)}
					<ListRow primary={a.path} primaryKind="path" primaryTip={fileTip(a)} facts={[{ value: bytes(a.sizeBytes) }]}>
						{#snippet leading()}<span class="icon"><Icon size={20} /></span>{/snippet}
						{#snippet action()}
							{#if a.url}<NeoButton text href={a.url} {...download} aria-label="Download">{#snippet icon()}<DownloadIcon size={20} />{/snippet}</NeoButton>{/if}
						{/snippet}
					</ListRow>
				{/each}
			</Stack>
			{#if model.warnings.length}
				<Collapse label="Warnings" count={model.warnings.length}>
					<Stack gap={4}>
						{#each model.warnings as w (w)}<Str value={w} kind="message" width="100%" />{/each}
					</Stack>
				</Collapse>
			{/if}
		</Stack>
	{/if}
{/snippet}

{#snippet fitView()}
	{#if resolveError}
		<Empty text={resolveError} action="Retry" onclick={() => attempt++} />
	{:else if !model}
		<Skeleton rows={3} height={36} />
	{:else}
		<Stack gap={12}>
			{#if groups.length > 1}<Select bind:value={groupKey} options={groupOptions} label="Group" />{/if}
			<FitTab {group} result={inspected} error={inspectError} {runtimes} launchLabel={stored ? 'Launch here' : 'Pull'} onlaunch={fitLaunch} />
		</Stack>
	{/if}
{/snippet}

{#snippet pullView()}
	{#if resolveError}
		<Empty text={resolveError} action="Retry" onclick={() => attempt++} />
	{:else if !model}
		<Skeleton rows={3} height={36} />
	{:else}
		<Stack gap={16}>
			{#if groups.length > 1}<Select bind:value={groupKey} options={groupOptions} label="Group" />{/if}
			{#if live.inMesh}
				<Picker bind:value={nodeId} fetch={fetchNodes} label="Node" placeholder="This node" clearable current={nodeId ? live.nodes.get(nodeId)?.name : undefined} />
			{/if}
			<Switch bind:checked={alone} label="Alone" info="Downloads this group without the parts its blueprint names" />
			{#if partsError}
				<p class="error-text">{partsError}</p>
			{:else if !partsRes}
				<Skeleton rows={2} height={36} />
			{:else if missing.length}
				<Checklist
					bind:value={chosen}
					options={missing.map((p) => ({ value: p.slot, label: p.label || p.slot, description: [p.name, p.sizeBytes ? bytes(p.sizeBytes) : ''].filter(Boolean).join(' · '), disabled: p.error !== '', tip: p.error || [p.repo, p.group].filter(Boolean).join(' ') || undefined }))}
					label="Parts"
					disabled={alone}
				/>
			{/if}
			<Row gap={12} wrap={false}>
				<Cell grow={false}>
					<Tip text="group {bytes(filesBytes)}\nparts {bytes(partsBytes)}" tag="span">
						<span class="total"><span class="secondary">total</span><span class="num">{bytes(filesBytes + partsBytes)}</span></span>
					</Tip>
				</Cell>
				<Cell grow={false} push>
					<Row gap={12} wrap={false}>
						{#if stored}
							<NeoButton text href={launchHref(ref)}>Launch</NeoButton>
						{/if}
						<NeoButton elevation={2} loading={pulling} onclick={() => pull(false)}>Pull</NeoButton>
						<NeoButton class="primary" elevation={2} loading={pulling} onclick={() => pull(true)}>Pull and launch</NeoButton>
					</Row>
				</Cell>
			</Row>
		</Stack>
	{/if}
{/snippet}

{#snippet tabView()}
	<PageTabs {tabs} active={activeTab} onchange={(id) => (tab = id)} />
	{#if activeTab === 'card'}
		{@render cardView()}
	{:else if activeTab === 'files'}
		{@render filesView()}
	{:else if activeTab === 'fit'}
		{@render fitView()}
	{:else}
		{@render pullView()}
	{/if}
{/snippet}

<Stack gap={16}>
	{#if headError}
		<Empty text={headError} action="Retry" onclick={loadHead} />
	{:else if !headLoaded}
		<Skeleton rows={6} />
	{:else if !source}
		<Empty text="No such source" action="Discover" href={back} />
	{:else}
		{@render header()}
		{#if viewport.ultrawide}
			<Split ratio="3/5">
				{#snippet left()}{@render cardView()}{/snippet}
				{#snippet right()}{@render tabView()}{/snippet}
			</Split>
		{:else}
			{@render tabView()}
		{/if}
	{/if}
</Stack>

<style>
	.title {
		display: flex;
		min-width: 0;
	}
	.icon {
		display: inline-flex;
		color: var(--nebu-text-muted);
	}
	.total {
		display: inline-flex;
		gap: 8px;
		align-items: baseline;
	}
</style>
