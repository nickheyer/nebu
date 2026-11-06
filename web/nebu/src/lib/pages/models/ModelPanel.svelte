<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import { NeoPopConfirm } from '#lib/neo/floating/tooltips/index.js';
	import { goto } from '$app/navigation';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, bytes, enumLabel, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { InspectResponse } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { ArtifactRole, TensorGroupKind, TensorGroupKindSchema } from '#lib/proto/nebu/v1/model_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import type { StoredArtifact, StoredModel } from '#lib/proto/nebu/v1/store_pb.js';
	import Bar from '#lib/ui/Bar.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import FitTab from '#lib/pages/models/FitTab.svelte';
	import PartsTab from '#lib/pages/models/PartsTab.svelte';
	import { kindLabel, launchHref, modelKeyOf, paramCount, removeModel, roleIcon, sameModel, verifyModel } from '#lib/pages/models/models.js';
	import { terminal } from '#lib/pages/serve/serve.js';
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
	// Anchor attributes the button passes through to the link it renders
	const download: Record<string, string> = { download: '' };
	const d = $derived(m.descriptor);
	const running = $derived([...live.instances.values()].some((i) => !terminal(i.state) && sameModel(i, m)));
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
	let removeOpen = $state(false);
	let inspected = $state<InspectResponse | undefined>(undefined);
	let inspectError = $state('');
	let inspectedKey = '';

	// One Inspect per group and revision feeds the Fit rows and the Files warnings, asked for once either tab opens
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
			{ label: 'format', value: art?.formatId || 'none' },
			...(art?.shardCount ? [{ label: 'shard', value: `${art.shardIndex} of ${art.shardCount}` }] : []),
			{ label: 'digest', value: a.digest }
		];
	}
</script>

<PanelHeader title={m.repo} kind="repo" subtitle={m.group || undefined} {onclose}>
	{#snippet tools()}
		<NeoButton class="primary" elevation={2} href={launchHref(m)}>Launch</NeoButton>
	{/snippet}
</PanelHeader>
<PageTabs {tabs} active={tab} onchange={(id) => (tab = id)} />
<div class="body">
	{#if tab === 'about'}
		<Stack gap={16}>
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
				<Stack gap={4}>
					<span class="label">Tensor groups</span>
					<Bar segments={groups} total={groups.reduce((n, g) => n + g.bytes, 0)} format={(n) => bytes(n)} />
				</Stack>
			{/if}
			<KeyRows
				rows={[
					{ label: 'store path', value: m.path, mono: true, copy: true, tip: m.path },
					{ label: 'pulled', value: ago(m.pulledAt), tip: when(m.pulledAt) },
					{ label: 'used', value: m.usedAt ? ago(m.usedAt) : 'never', tip: when(m.usedAt) || undefined },
					{ label: 'commit', value: m.commit, mono: true, copy: m.commit !== '', tip: m.commit }
				]}
			/>
			{#if d && Object.keys(d.metadata).length}
				<Collapse label="Metadata">
					<KeyRows dense rows={Object.entries(d.metadata).map(([k, v]) => ({ label: k, value: v, mono: true, tip: v }))} />
				</Collapse>
			{/if}
			<Row gap={12} justify="flex-end">
				<NeoButton text onclick={() => verifyModel(m)}>Verify</NeoButton>
				<NeoButton text onclick={() => onexport(m)}>Export</NeoButton>
				<Tip text={running ? 'An instance runs this group' : undefined} tag="span">
					<NeoPopConfirm bind:open={removeOpen} elevation={2} tooltipProps={{ placement: 'bottom-end' }} header="Remove {m.repo}" onConfirm={remove} confirmProps={{ label: 'Remove', color: 'error' }}>
						{#snippet tooltip()}<div class="confirm"><Switch bind:checked={collect} label="Collect unreferenced blobs" /></div>{/snippet}
						<NeoButton text color="error" disabled={running} onclick={() => (removeOpen = true)}>Remove</NeoButton>
					</NeoPopConfirm>
				</Tip>
			</Row>
		</Stack>
	{:else if tab === 'files'}
		<Stack gap={12}>
			<Stack gap={0}>
				{#each m.artifacts as a (a.path)}
					{@const art = a.artifact}
					{@const Icon = roleIcon(art?.role ?? ArtifactRole.OTHER)}
					<ListRow primary={art?.path ?? a.path} primaryKind="path" primaryTip={fileFacts(a).map((f) => `${f.label} ${f.value}`).join('\n')} facts={[{ value: bytes(art?.sizeBytes) }]}>
						{#snippet leading()}<span class="icon"><Icon size={20} /></span>{/snippet}
						{#snippet action()}
							{#if art?.url}
								<NeoButton text href={art.url} {...download} aria-label="Download">{#snippet icon()}<DownloadIcon size={20} />{/snippet}</NeoButton>
							{/if}
						{/snippet}
					</ListRow>
				{/each}
			</Stack>
			{#if inspectError}
				<p class="error-text">{inspectError}</p>
			{:else if inspected?.model?.warnings.length}
				<Collapse label="Warnings" count={inspected.model.warnings.length}>
					<Stack gap={4}>
						{#each inspected.model.warnings as w (w)}<Str value={w} kind="message" width="100%" />{/each}
					</Stack>
				</Collapse>
			{/if}
		</Stack>
	{:else if tab === 'fit'}
		<FitTab group={m.group} result={inspected} error={inspectError} {runtimes} onlaunch={(runtimeId, context) => goto(launchHref(m, runtimeId, context))} />
	{:else if tab === 'parts'}
		<PartsTab model={m} />
	{/if}
</div>

<style>
	.body {
		margin-top: 12px;
	}
	.confirm {
		padding: 4px 4px 8px;
	}
	.icon {
		display: inline-flex;
		flex: 0 0 auto;
		color: var(--nebu-text-muted);
	}
</style>
