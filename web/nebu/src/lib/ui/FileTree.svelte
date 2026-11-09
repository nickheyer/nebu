<script lang="ts" module>
	import type { Artifact } from '#lib/proto/nebu/v1/model_pb.js';

	export type FileNode = { name: string; path: string; dirs: FileNode[]; files: Artifact[]; bytes: number; count: number };
	// One row of the tree as its action sees it, every file under a directory
	export type TreeItem = { path: string; name: string; files: Artifact[]; dir: boolean };

	// Every file as a directory tree, directories first and each level sorted by name
	export function fileTree(artifacts: Artifact[]): FileNode {
		const root: FileNode = { name: '', path: '', dirs: [], files: [], bytes: 0, count: 0 };
		for (const a of artifacts) {
			const parts = a.path.split('/');
			let node = root;
			for (const part of parts.slice(0, -1)) {
				let next = node.dirs.find((d) => d.name === part);
				if (!next) {
					next = { name: part, path: node.path ? `${node.path}/${part}` : part, dirs: [], files: [], bytes: 0, count: 0 };
					node.dirs.push(next);
				}
				node = next;
			}
			node.files.push(a);
		}
		const sum = (n: FileNode) => {
			n.dirs.sort((x, y) => x.name.localeCompare(y.name));
			n.files.sort((x, y) => x.path.localeCompare(y.path));
			for (const d of n.dirs) sum(d);
			n.bytes = n.dirs.reduce((t, d) => t + d.bytes, 0) + n.files.reduce((t, f) => t + Number(f.sizeBytes), 0);
			n.count = n.dirs.reduce((t, d) => t + d.count, 0) + n.files.length;
		};
		sum(root);
		return root;
	}

	export function treeFiles(node: FileNode): Artifact[] {
		return [...node.files, ...node.dirs.flatMap(treeFiles)];
	}

	export function baseName(path: string): string {
		return path.slice(path.lastIndexOf('/') + 1);
	}
</script>

<script lang="ts">
	import type { Snippet } from 'svelte';
	import { untrack } from 'svelte';
	import { SvelteSet } from 'svelte/reactivity';
	import { bytes, plural } from '#lib/format.js';
	import Empty from '#lib/ui/Empty.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import { roleIcon } from '#lib/pages/models/models.js';
	import CaretRightIcon from 'phosphor-svelte/lib/CaretRightIcon';
	import CheckCircleIcon from 'phosphor-svelte/lib/CheckCircleIcon';
	import FilesIcon from 'phosphor-svelte/lib/FilesIcon';
	import FolderIcon from 'phosphor-svelte/lib/FolderIcon';
	import FolderOpenIcon from 'phosphor-svelte/lib/FolderOpenIcon';

	// Every file as a tree with its size, a check on stored files and one action per row
	let { artifacts, stored = new Set<string>(), action }: { artifacts: Artifact[]; stored?: Set<string>; action?: Snippet<[TreeItem]> } = $props();

	const root = $derived(fileTree(artifacts));
	const open = new SvelteSet<string>();

	function allDirs(node: FileNode): string[] {
		return node.dirs.flatMap((d) => [d.path, ...allDirs(d)]);
	}

	// A small tree starts fully open, a large one with its directories closed
	$effect(() => {
		const r = root;
		untrack(() => {
			open.clear();
			if (r.count <= 24) for (const p of allDirs(r)) open.add(p);
		});
	});

	function toggle(path: string) {
		if (open.has(path)) open.delete(path);
		else open.add(path);
	}
</script>

{#snippet branch(node: FileNode, depth: number)}
	{#each node.dirs as d (d.path)}
		{@const isOpen = open.has(d.path)}
		<div class="row" style:--depth={depth}>
			<button class="twist" type="button" aria-label={isOpen ? 'Collapse' : 'Expand'} aria-expanded={isOpen} onclick={() => toggle(d.path)}>
				<span class="caret" class:open={isOpen}><CaretRightIcon size={14} /></span>
			</button>
			<span class="icon">{#if isOpen}<FolderOpenIcon size={18} />{:else}<FolderIcon size={18} />{/if}</span>
			<button class="name dir" type="button" onclick={() => toggle(d.path)}>{d.name}</button>
			<span class="meta">{plural(d.count, 'file')}</span>
			<span class="size">{bytes(d.bytes)}</span>
			{#if action}<span class="act">{@render action({ path: d.path, name: d.name, files: treeFiles(d), dir: true })}</span>{/if}
		</div>
		{#if isOpen}{@render branch(d, depth + 1)}{/if}
	{/each}
	{#each node.files as f (f.path)}
		{@const Icon = roleIcon(f.role)}
		<div class="row" style:--depth={depth}>
			<span class="twist"></span>
			<span class="icon"><Icon size={18} /></span>
			<span class="name mono">{baseName(f.path)}</span>
			{#if stored.has(f.path)}<Tip text="Stored" tag="span"><span class="stored"><CheckCircleIcon size={16} weight="fill" /></span></Tip>{/if}
			<span class="size">{bytes(f.sizeBytes)}</span>
			{#if action}<span class="act">{@render action({ path: f.path, name: baseName(f.path), files: [f], dir: false })}</span>{/if}
		</div>
	{/each}
{/snippet}

{#if root.count === 0}
	<Empty icon={FilesIcon} text="No files" />
{:else}
	<div class="tree">
		<div class="head listhead" aria-hidden="true">
			<span class="headname">File</span>
			<span class="size">Size</span>
			{#if action}<span class="act"></span>{/if}
		</div>
		{@render branch(root, 0)}
	</div>
{/if}

<style>
	.tree {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.head {
		display: flex;
		gap: 8px;
		align-items: center;
		min-height: 34px;
		padding: 0 var(--nebu-row-inset, var(--nebu-space-2));
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-muted);
		white-space: nowrap;
		background: var(--nebu-surface-sunken);
		border-top: 1px solid var(--nebu-border);
		border-bottom: 1px solid var(--nebu-border);
	}
	.headname {
		flex: 1 1 auto;
	}
	.row {
		display: flex;
		gap: 8px;
		align-items: center;
		min-height: 40px;
		padding: 2px var(--nebu-row-inset, var(--nebu-space-2)) 2px calc(var(--nebu-row-inset, var(--nebu-space-2)) + var(--depth) * 20px);
		border-bottom: 1px solid var(--nebu-border);
		animation: nebu-reveal 220ms var(--nebu-settle) backwards;
	}
	.row:last-child {
		border-bottom-color: transparent;
	}
	.twist {
		display: inline-flex;
		flex: 0 0 20px;
		justify-content: center;
		padding: 4px 0;
		color: var(--nebu-text-muted);
		background: none;
		border: 0;
	}
	button.twist {
		cursor: pointer;
	}
	.caret {
		display: inline-flex;
		transition: transform var(--nebu-normal) var(--nebu-settle);
	}
	.caret.open {
		transform: rotate(90deg);
	}
	.icon {
		display: inline-flex;
		flex: 0 0 auto;
		color: var(--nebu-text-muted);
	}
	.name {
		flex: 1 1 auto;
		min-width: 0;
		text-align: left;
		overflow-wrap: anywhere;
	}
	.dir {
		padding: 0;
		font: inherit;
		color: inherit;
		background: none;
		border: 0;
		cursor: pointer;
	}
	.dir:hover {
		color: var(--neo-color-primary);
	}
	.mono {
		font-family: var(--nebu-font-mono);
		font-size: 13px;
	}
	.stored {
		display: inline-flex;
		flex: 0 0 auto;
		color: var(--neo-color-success);
	}
	.meta {
		flex: 0 0 auto;
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
	.size {
		flex: 0 0 auto;
		min-width: 7ch;
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
		text-align: right;
		white-space: nowrap;
	}
	.act {
		display: inline-flex;
		flex: 0 0 36px;
		justify-content: flex-end;
	}
</style>
