<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes } from '#lib/format.js';
	import type { DirEntry } from '#lib/proto/nebu/v1/host_pb.js';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import ArrowUpIcon from 'phosphor-svelte/lib/ArrowUpIcon';
	import FileIcon from 'phosphor-svelte/lib/FileIcon';
	import FolderIcon from 'phosphor-svelte/lib/FolderIcon';

	export type BrowseFilter = 'any' | 'directories' | 'executables';

	let {
		open = $bindable(false),
		start = '',
		filter = 'any',
		onpick
	}: { open?: boolean; start?: string; filter?: BrowseFilter; onpick: (path: string) => void } = $props();

	let path = $state('');
	let parent = $state('');
	let entries = $state<DirEntry[]>([]);
	let typed = $state('');
	let error = $state<string | null>(null);
	let loading = $state(false);

	const shown = $derived(entries.filter((e) => filter === 'any' || e.dir || (filter === 'executables' && e.executable)));
	const crumbs = $derived(path.split(/[\\/]/).filter(Boolean));
	const separator = $derived(path.includes('\\') && !path.includes('/') ? '\\' : '/');

	$effect(() => {
		if (open) void go(start);
	});

	async function go(target: string) {
		loading = true;
		try {
			const res = await rpc.host.listDirectory({ path: target });
			path = res.path;
			parent = res.parent;
			entries = res.entries;
			typed = res.path;
			error = null;
		} catch (err) {
			error = reason(err);
		} finally {
			loading = false;
		}
	}

	function join(name: string): string {
		return path.endsWith(separator) ? path + name : path + separator + name;
	}

	function crumbPath(i: number): string {
		const head = path.startsWith('/') ? '/' : '';
		return head + crumbs.slice(0, i + 1).join(separator);
	}

	function choose(p: string) {
		onpick(p);
		open = false;
	}

	function pick(e: DirEntry) {
		if (e.dir) void go(join(e.name));
		else if (filter !== 'directories') choose(join(e.name));
	}
</script>

{#snippet folder()}<FolderIcon size={20} />{/snippet}
{#snippet file()}<FileIcon size={20} />{/snippet}

<NeoDialog bind:open modal rounded="md" filled elevation={2} width="min(40rem, 94vw)" padding="16px">
	<div class="browser">
		<div class="crumbs">
			{#each crumbs as c, i (i)}
				<NeoButton text onclick={() => go(crumbPath(i))}><span class="mono">{c}</span></NeoButton>
				{#if i < crumbs.length - 1}<span class="secondary">/</span>{/if}
			{/each}
			<span class="fill"></span>
			<NeoButton text disabled={!parent} onclick={() => go(parent)}>
				{#snippet icon()}<ArrowUpIcon size={20} />{/snippet}
				Up
			</NeoButton>
		</div>
		<Text bind:value={typed} mono placeholder="Path" onchange={(v) => go(v)} />
		{#if error}<p class="error-text secondary">{error}</p>{/if}
		<div class="list" aria-busy={loading}>
			{#each shown as e (e.name)}
				<ListRow primary={e.name} primaryKind="path" facts={e.dir ? [] : [{ value: bytes(e.sizeBytes) }]} leading={e.dir ? folder : file} onclick={() => pick(e)} />
			{/each}
			{#if shown.length === 0 && !loading}
				<p class="secondary pad">Nothing here</p>
			{/if}
		</div>
		<div class="actions">
			<NeoButton text onclick={() => (open = false)}>Cancel</NeoButton>
			{#if filter === 'directories' || filter === 'any'}
				<NeoButton class="primary" elevation={2} onclick={() => choose(path)}>Choose this directory</NeoButton>
			{/if}
		</div>
	</div>
</NeoDialog>

<style>
	.browser {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.crumbs {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
		align-items: center;
	}
	.list {
		display: flex;
		flex-direction: column;
		max-height: 50vh;
		overflow: auto;
	}
	.pad {
		padding: 12px;
	}
	.actions {
		display: flex;
		gap: 12px;
		justify-content: flex-end;
	}
</style>
