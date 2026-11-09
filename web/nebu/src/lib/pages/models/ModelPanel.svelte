<script lang="ts">
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, bytes, enumLabel, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { InspectResponse } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { TensorGroupKind, TensorGroupKindSchema } from '#lib/proto/nebu/v1/model_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import type { StoredModel } from '#lib/proto/nebu/v1/store_pb.js';
	import Bar from '#lib/ui/Bar.svelte';
	import Collapse from '#lib/ui/Collapse.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import FileTree, { type TreeItem } from '#lib/ui/FileTree.svelte';
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
	import { kindLabel, launchHref, modelKeyOf, paramCount, removeModel, sameModel, verifyModel } from '#lib/pages/models/models.js';
	import { terminal } from '#lib/pages/serve/serve.js';

	let {
		model,
		runtimes,
		tab = $bindable('about'),
		onclose,
		onexport,
		onremoved
	}: { model: StoredModel; runtimes: RuntimeStatus[]; tab?: string; onclose: () => void; onexport: (m: StoredModel) => void; onremoved: () => void } = $props();

	const m = $derived(model);
	const d = $derived(m.descriptor);
	const running = $derived([...live.instances.values()].some((i) => !terminal(i.state) && sameModel(i, m)));
	const files = $derived(m.artifacts.map((a) => a.artifact).filter((a) => a !== undefined));
	// Where each file's link sits on this host's disk, by its path in the repository
	const onDisk = $derived(new Map(m.artifacts.map((a) => [a.artifact?.path ?? '', a.path])));
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
</script>

{#snippet copyPath(item: TreeItem)}
	<CopyButton text={item.dir ? `${m.path}/${item.path}` : (onDisk.get(item.path) ?? `${m.path}/${item.path}`)} label="Copy path" />
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
						{#snippet body()}<Switch bind:checked={collect} label="Collect blobs" info="Deletes the blobs no other model shares" />{/snippet}
					</Confirm>
				</Tip>
			</div>
		</Stack>
	{:else if tab === 'files'}
		<FileTree artifacts={files} action={copyPath} />
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
			<Switch bind:checked={free} label="Against free memory" info="Plans against the memory left beside what runs now instead of each pool's total" />
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
</style>
