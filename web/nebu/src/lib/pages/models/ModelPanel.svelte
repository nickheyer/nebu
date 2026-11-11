<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { ago, bytes, enumLabel, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { TensorGroupKind, TensorGroupKindSchema } from '#lib/proto/nebu/v1/model_pb.js';
	import type { StoredModel } from '#lib/proto/nebu/v1/store_pb.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import FileTree, { type TreeItem } from '#lib/ui/FileTree.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import SplitBar from '#lib/ui/SplitBar.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { repoPath } from '#lib/pages/discover/discover.js';
	import type { Contexts } from '#lib/pages/models/context.svelte.js';
	import { launchHref, paramCount, removeModel, sameModel, verifyModel } from '#lib/pages/models/models.js';
	import { modelPhrase, terminal } from '#lib/pages/serve/serve.js';

	// One stored model, what its weights are made of and every file it holds
	let {
		model,
		contexts,
		tab = $bindable('about'),
		onclose,
		onexport,
		onremoved
	}: { model: StoredModel; contexts: Contexts; tab?: string; onclose: () => void; onexport: (m: StoredModel) => void; onremoved: () => void } = $props();

	const m = $derived(model);
	const d = $derived(m.descriptor);
	const running = $derived([...live.instances.values()].some((i) => !terminal(i.state) && sameModel(i, m)));
	const files = $derived(m.artifacts.map((a) => a.artifact).filter((a) => a !== undefined));
	// Where each file's link sits on this host's disk, by its path in the repository
	const onDisk = $derived(new Map(m.artifacts.map((a) => [a.artifact?.path ?? '', a.path])));
	const tabs = [
		{ id: 'about', label: 'About' },
		{ id: 'files', label: 'Files' }
	];

	// Tensor bytes summed by what the tensors are, largest first
	const shares = $derived.by(() => {
		const sums = new Map<TensorGroupKind, number>();
		for (const g of d?.groups ?? []) sums.set(g.kind, (sums.get(g.kind) ?? 0) + Number(g.bytes));
		return [...sums.entries()].sort((a, b) => b[1] - a[1]).map(([kind, n]) => ({ label: enumLabel(TensorGroupKindSchema, kind), value: n }));
	});

	// The other stored models holding the very same file, by each file's path here
	const shared = $derived.by(() => {
		const holders = new Map<string, string[]>();
		for (const other of live.models.values()) {
			if (sameModel(other, m)) continue;
			for (const a of other.artifacts) if (a.digest) holders.set(a.digest, [...(holders.get(a.digest) ?? []), modelPhrase(other)]);
		}
		const out = new Map<string, string[]>();
		for (const a of m.artifacts) {
			const names = a.digest ? holders.get(a.digest) : undefined;
			if (names && a.artifact) out.set(a.artifact.path, names);
		}
		return out;
	});

	const ref = $derived({ sourceId: m.sourceId, repo: m.repo, revision: m.revision, group: m.group });
	$effect(() => contexts.want(ref));
	const planned = $derived(contexts.get(ref));
	const warnings = $derived(planned?.error ? [planned.error] : (planned?.warnings ?? []));

	let collect = $state(true);

	async function remove() {
		if (await removeModel(m, collect)) onremoved();
	}
</script>

{#snippet copyPath(item: TreeItem)}
	<CopyButton text={item.dir ? `${m.path}/${item.path}` : (onDisk.get(item.path) ?? `${m.path}/${item.path}`)} label="Copy path" />
{/snippet}

<PanelHeader title={m.repo} kind="repo" subtitle={m.group || undefined} href={repoPath(m.sourceId, m.repo)} {onclose}>
	{#snippet tools()}
		{#if m.runtimes}<NeoButton class="primary" elevation={2} href={launchHref(m)}>Launch</NeoButton>{/if}
	{/snippet}
</PanelHeader>
<PageTabs {tabs} active={tab} onchange={(id) => (tab = id)} />
<div class="body">
	{#if tab === 'files'}
		<FileTree artifacts={files} {shared} action={copyPath} />
	{:else}
		<Stack gap={24}>
			<KeyRows
				rows={[
					{ label: 'architecture', value: d?.architecture ?? '' },
					{ label: 'parameters', value: paramCount(d?.parameterCount) },
					{ label: 'bits per weight', value: d?.bitsPerWeight ? d.bitsPerWeight.toFixed(2) : '' },
					{ label: 'size', value: bytes(m.bytes) },
					{ label: 'path', value: m.path, mono: true, copy: true },
					{ label: 'pulled', value: ago(m.pulledAt), tip: when(m.pulledAt) },
					{ label: 'used', value: m.usedAt ? ago(m.usedAt) : '', tip: when(m.usedAt) || undefined },
					{ label: 'commit', value: m.commit, mono: true, copy: m.commit !== '' }
				]}
			/>
			{#if warnings.length}
				<List>
					{#each warnings as w (w)}
						<ListRow lamp={planned?.error ? 'error' : 'warning'} primary={w} primaryKind="message" />
					{/each}
				</List>
			{/if}
			{#if shares.length > 1}
				<Section title="Weights" level={3}>
					<SplitBar {shares} format={(n) => bytes(n)} />
				</Section>
			{/if}
			{#if d && Object.keys(d.metadata).length}
				<Collapse label="Header">
					<KeyRows dense rows={Object.entries(d.metadata).map(([k, v]) => ({ label: k, value: v, mono: true }))} />
				</Collapse>
			{/if}
			<div class="actions">
				<Tip text={running ? 'Running' : undefined} tag="span">
					<Confirm label="Remove" question="Remove {m.repo}{m.group ? ` ${m.group}` : ''}?" danger disabled={running} placement="top-start" onconfirm={remove}>
						{#snippet body()}<Switch bind:checked={collect} label="Delete its files" info="Files another model uses stay" compact />{/snippet}
					</Confirm>
				</Tip>
				<span class="fill"></span>
				<NeoButton text onclick={() => verifyModel(m)}>Verify</NeoButton>
				<NeoButton text onclick={() => onexport(m)}>Export</NeoButton>
			</div>
		</Stack>
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
		align-items: center;
	}
</style>
