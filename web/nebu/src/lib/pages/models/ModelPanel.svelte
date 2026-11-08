<script lang="ts">
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, bytes, enumLabel, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { InspectResponse } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { ArtifactRole, TensorGroupKind, TensorGroupKindSchema } from '#lib/proto/nebu/v1/model_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import type { StoredArtifact, StoredModel } from '#lib/proto/nebu/v1/store_pb.js';
	import Bar from '#lib/ui/Bar.svelte';
	import Collapse from '#lib/ui/Collapse.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import FitTab from '#lib/pages/models/FitTab.svelte';
	import PartsTab from '#lib/pages/models/PartsTab.svelte';
	import ShardGrid from '#lib/pages/models/ShardGrid.svelte';
	import { kindLabel, launchHref, modelKeyOf, paramCount, removeModel, roleIcon, sameModel, shardPattern, verifyModel } from '#lib/pages/models/models.js';
	import { terminal } from '#lib/pages/serve/serve.js';
	import CubeIcon from 'phosphor-svelte/lib/CubeIcon';
	import DownloadIcon from 'phosphor-svelte/lib/DownloadIcon';

	let {
		model,
		runtimes,
		tab = $bindable('about'),
		onclose,
		onexport,
		onremoved
	}: { model: StoredModel; runtimes: RuntimeStatus[]; tab?: string; onclose: () => void; onexport: (m: StoredModel) => void; onremoved: () => void } = $props();

	const m = $derived(model);
	const download: Record<string, string> = { download: '' };
	const d = $derived(m.descriptor);
	const running = $derived([...live.instances.values()].some((i) => !terminal(i.state) && sameModel(i, m)));
	const weightFiles = $derived(m.artifacts.filter((a) => a.artifact?.role === ArtifactRole.WEIGHTS).sort((x, y) => (x.artifact?.shardIndex ?? 0) - (y.artifact?.shardIndex ?? 0) || x.path.localeCompare(y.path)));
	const otherFiles = $derived(m.artifacts.filter((a) => a.artifact?.role !== ArtifactRole.WEIGHTS));
	const tabs = [
		{ id: 'about', label: 'About' },
		{ id: 'files', label: 'Files' },
		{ id: 'fit', label: 'Fit' },
		{ id: 'parts', label: 'Parts' }
	];

	// Tensor bytes summed by group kind for the stacked bar
	const groups = $derived.by(() => {
		const sums = new Map<TensorGroupKind, number>();
		for (const g of d?.groups ?? []) sums.set(g.kind, (sums.get(g.kind) ?? 0) + Number(g.bytes));
		return [...sums.entries()].sort((a, b) => b[1] - a[1]).map(([kind, n]) => ({ kind: 'instance' as const, label: enumLabel(TensorGroupKindSchema, kind), bytes: n }));
	});

	let collect = $state(true);
	let free = $state(false);
	let inspected = $state<InspectResponse | undefined>(undefined);
	let inspectError = $state('');
	let inspectedKey = '';

	// One Inspect per group and revision feeds the Fit rows and the Files warnings
	$effect(() => {
		const key = `${modelKeyOf(m)}@${m.revision}`;
		if ((tab !== 'fit' && tab !== 'files') || key === inspectedKey) return;
		inspectedKey = key;
		inspected = undefined;
		inspectError = '';
		rpc.estimate
			.inspect({ sourceId: m.sourceId, repo: m.repo, revision: m.revision, groups: [m.group] })
			.then((res) => {
				if (inspectedKey === key) inspected = res;
			})
			.catch((err) => {
				if (inspectedKey === key) inspectError = reason(err);
			});
	});

	async function remove() {
		if (await removeModel(m, collect)) onremoved();
	}

	function fileFacts(a: StoredArtifact) {
		const art = a.artifact;
		return [
			{ label: 'path', value: art?.path ?? a.path },
			{ label: 'format', value: art?.formatId ?? '' },
			...(art?.shardCount ? [{ label: 'shard', value: `${art.shardIndex} of ${art.shardCount}` }] : []),
			{ label: 'digest', value: a.digest }
		];
	}
</script>

{#snippet storedRow(a: StoredArtifact, marked: boolean)}
	{@const art = a.artifact}
	{@const Icon = roleIcon(art?.role ?? ArtifactRole.OTHER)}
	{#snippet fileDownload()}
		<NeoButton text href={art?.url} {...download} aria-label="Download {a.path}">{#snippet icon()}<DownloadIcon size={20} />{/snippet}</NeoButton>
	{/snippet}
	{#snippet roleMark()}<span class="icon"><Icon size={20} /></span>{/snippet}
	<ListRow primary={art?.path ?? a.path} primaryKind="path" primaryTip={fileFacts(a).map((f) => `${f.label} ${f.value}`).join('\n')} facts={[{ value: bytes(art?.sizeBytes) }]} action={art?.url ? fileDownload : undefined} leading={marked ? roleMark : undefined} />
{/snippet}

<PanelHeader title={m.repo} kind="repo" subtitle={m.group || undefined} {onclose}>
	{#snippet tools()}
		<NeoButton class="primary" elevation={2} href={launchHref(m)}>Launch</NeoButton>
	{/snippet}
</PanelHeader>
<PageTabs {tabs} active={tab} onchange={(id) => (tab = id)} />
<div class="body">
	{#if tab === 'about'}
		<Stack gap={24}>
			<KeyRows
				rows={[
					{ label: 'architecture', value: d?.architecture ?? '' },
					{ label: 'family', value: d?.family ?? '' },
					{ label: 'parameters', value: paramCount(d?.parameterCount) },
					{ label: 'bits per weight', value: d?.bitsPerWeight ? d.bitsPerWeight.toFixed(2) : '' },
					{ label: 'kind', value: kindLabel(d?.kind ?? 0) },
					{ label: 'generates', value: d?.generates.join(', ') ?? '' }
				]}
			/>
			{#if groups.length}
				<Section title="Tensor groups" level={3}>
					<Bar segments={groups} total={groups.reduce((n, g) => n + g.bytes, 0)} format={(n) => bytes(n)} />
				</Section>
			{/if}
			<Section title="Store" level={3}>
				<KeyRows
					rows={[
						{ label: 'path', value: m.path, mono: true, copy: true, tip: m.path },
						{ label: 'pulled', value: ago(m.pulledAt), tip: when(m.pulledAt) },
						{ label: 'used', value: m.usedAt ? ago(m.usedAt) : '', tip: when(m.usedAt) || undefined },
						{ label: 'commit', value: m.commit, mono: true, copy: m.commit !== '', tip: m.commit }
					]}
				/>
			</Section>
			{#if d && Object.keys(d.metadata).length}
				<Collapse label="Metadata">
					<KeyRows dense rows={Object.entries(d.metadata).map(([k, v]) => ({ label: k, value: v, mono: true, tip: v }))} />
				</Collapse>
			{/if}
			<div class="actions">
				<NeoButton text onclick={() => verifyModel(m)}>Verify</NeoButton>
				<NeoButton text onclick={() => onexport(m)}>Export</NeoButton>
				<Tip text={running ? 'Running' : undefined} tag="span">
					<Confirm label="Remove" question="Remove {m.repo}{m.group ? ` ${m.group}` : ''}?" danger disabled={running} onconfirm={remove}>
						{#snippet body()}<Switch bind:checked={collect} label="Collect blobs" />{/snippet}
					</Confirm>
				</Tip>
			</div>
		</Stack>
	{:else if tab === 'files'}
		<List head={{ primary: 'File', facts: ['Size'] }}>
			{#if weightFiles.length > 1}
				{#snippet shards()}
					<ShardGrid shards={weightFiles.map((a) => ({ path: a.artifact?.path ?? a.path, index: a.artifact?.shardIndex ?? 0, bytes: a.artifact?.sizeBytes ?? 0n, url: a.artifact?.url || undefined }))} />
				{/snippet}
				<ListRow primary={shardPattern(weightFiles.map((a) => a.artifact?.path ?? a.path))} primaryKind="path" facts={[{ value: bytes(weightFiles.reduce((n, a) => n + Number(a.artifact?.sizeBytes ?? 0n), 0)) }]} expand={shards}>
					{#snippet leading()}<span class="icon"><CubeIcon size={20} /></span>{/snippet}
				</ListRow>
			{:else}
				{#each weightFiles as a (a.path)}{@render storedRow(a, true)}{/each}
			{/if}
			{#each otherFiles as a (a.path)}{@render storedRow(a, true)}{/each}
		</List>
		{#if inspectError}
			<List head={{ primary: 'Warnings' }}>
				<ListRow lamp="error" primary={inspectError} primaryKind="message" />
			</List>
		{:else if inspected?.model?.warnings.length}
			<List head={{ primary: 'Warnings' }}>
				{#each inspected.model.warnings as w (w)}
					<ListRow lamp="warning" primary={w} primaryKind="message" />
				{/each}
			</List>
		{/if}
	{:else if tab === 'fit'}
		<Stack gap={12}>
			<Switch bind:checked={free} label="Against free memory" />
			<FitTab group={m.group} result={inspected} error={inspectError} {runtimes} {free} onlaunch={(runtimeId, context) => goto(launchHref(m, runtimeId, context))} />
		</Stack>
	{:else if tab === 'parts'}
		<PartsTab model={m} />
	{/if}
</div>

<style>
	.body {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-4);
		margin-top: var(--nebu-space-4);
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-3);
		justify-content: flex-end;
	}
	.icon {
		display: inline-flex;
		flex: 0 0 auto;
		color: var(--nebu-text-muted);
	}
</style>
