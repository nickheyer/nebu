<script lang="ts">
	import { tick } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';
	import { isCancel, reason, rpc } from '#lib/api/transport.js';
	import { bytes } from '#lib/format.js';
	import { DirPlaceKind, type DirEntry, type DirPlace, type DirStep } from '#lib/proto/nebu/v1/host_pb.js';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import ArrowUpIcon from 'phosphor-svelte/lib/ArrowUpIcon';
	import CubeIcon from 'phosphor-svelte/lib/CubeIcon';
	import FileIcon from 'phosphor-svelte/lib/FileIcon';
	import FolderIcon from 'phosphor-svelte/lib/FolderIcon';
	import HardDriveIcon from 'phosphor-svelte/lib/HardDriveIcon';
	import HouseIcon from 'phosphor-svelte/lib/HouseIcon';

	export type BrowseFilter = 'any' | 'files' | 'directories' | 'executables';
	export type BrowseRoot = 'home' | 'store';

	// Picks a file or directory on the host, opening on the held value or else on the root place
	let {
		open = $bindable(false),
		start = '',
		root = 'home',
		filter = 'any',
		onpick
	}: { open?: boolean; start?: string; root?: BrowseRoot; filter?: BrowseFilter; onpick: (path: string) => void } = $props();

	let path = $state('');
	let parent = $state('');
	let entries = $state<DirEntry[]>([]);
	let trail = $state<DirStep[]>([]);
	let places = $state<DirPlace[]>([]);
	let typed = $state('');
	let error = $state<string | null>(null);
	let loading = $state(false);
	let listEl = $state<HTMLElement>();
	let abort: AbortController | undefined;

	const shown = $derived(entries.filter((e) => e.dir || filter === 'any' || filter === 'files' || (filter === 'executables' && e.executable)));
	const picksDirectory = $derived(filter === 'directories' || filter === 'any');
	const nothing = $derived(filter === 'directories' ? 'No folders' : filter === 'executables' ? 'No folders or programs' : 'Empty');
	// The deepest step that is a place, the steps under it standing as the crumbs
	const anchor = $derived(trail.findLastIndex((s) => places.some((p) => p.path === s.path)));
	const here = $derived(anchor < 0 ? undefined : places.find((p) => p.path === trail[anchor]!.path));
	const crumbs = $derived(trail.slice(anchor + 1));

	$effect(() => {
		if (!open) return;
		path = '';
		entries = [];
		trail = [];
		error = null;
		void go(start, true);
		return () => abort?.abort();
	});

	// Lists a directory, the nearest one that lists when opening on a value that no longer does
	async function go(target: string, nearest = false) {
		abort?.abort();
		const ac = new AbortController();
		abort = ac;
		loading = true;
		try {
			const res = await rpc.host.listDirectory({ path: target, start: root === 'store' ? DirPlaceKind.STORE : DirPlaceKind.HOME, nearest }, { signal: ac.signal });
			path = res.path;
			parent = res.parent;
			entries = res.entries;
			trail = res.trail;
			places = res.places;
			typed = res.path;
			error = null;
			await tick();
			const held = listEl?.querySelector<HTMLElement>('.rowwrap.selected');
			if (listEl) listEl.scrollTop = held ? held.offsetTop - (listEl.clientHeight - held.offsetHeight) / 2 : 0;
		} catch (err) {
			if (isCancel(err) || ac.signal.aborted) return;
			error = reason(err);
		} finally {
			if (abort === ac) loading = false;
		}
	}

	function placeLabel(p: DirPlace): string {
		return p.kind === DirPlaceKind.STORE ? 'Store' : p.kind === DirPlaceKind.HOME ? 'Home' : p.path;
	}

	function choose(p: string) {
		onpick(p);
		open = false;
	}

	function pick(e: DirEntry) {
		if (e.dir) void go(e.path);
		else choose(e.path);
	}
</script>

{#snippet folder()}<FolderIcon size={20} />{/snippet}
{#snippet file()}<FileIcon size={20} />{/snippet}

<NeoDialog bind:open modal rounded="md" filled elevation={2} width="min(44rem, 94vw)" padding="16px">
	<div class="browser">
		<div class="bar">
			{#each places as p (p.path)}
				<NeoButton text color={p === here ? 'primary' : undefined} aria-current={p === here ? 'location' : undefined} onclick={() => go(p.path)}>
					{#snippet icon()}
						{#if p.kind === DirPlaceKind.STORE}<CubeIcon size={18} />{:else if p.kind === DirPlaceKind.HOME}<HouseIcon size={18} />{:else}<HardDriveIcon size={18} />{/if}
					{/snippet}
					<span class:mono={p.kind === DirPlaceKind.VOLUME}>{placeLabel(p)}</span>
				</NeoButton>
			{/each}
			<span class="fill"></span>
			<NeoButton text disabled={!parent} onclick={() => go(parent)}>
				{#snippet icon()}<ArrowUpIcon size={18} />{/snippet}
				Up
			</NeoButton>
		</div>
		{#if crumbs.length}
			<div class="bar crumbs">
				{#each crumbs as step, i (step.path)}
					{#if i > 0}<span class="secondary">/</span>{/if}
					<NeoButton text onclick={() => go(step.path)}><span class="mono">{step.name}</span></NeoButton>
				{/each}
			</div>
		{/if}
		<Text bind:value={typed} mono label="Path" onchange={(v) => go(v)} />
		{#if error}<p class="error-text secondary">{error}</p>{/if}
		<div class="list" class:busy={loading} aria-busy={loading} bind:this={listEl}>
			{#if loading && path === ''}
				<Skeleton rows={6} dot={false} leading="20px" secondary={false} facts={0} action={false} menu={false} />
			{:else}
				{#each shown as e (e.path)}
					<ListRow primary={e.name} primaryKind="path" facts={e.dir ? [] : [{ value: bytes(e.sizeBytes) }]} leading={e.dir ? folder : file} selected={e.path === start} onclick={() => pick(e)} />
				{/each}
				{#if shown.length === 0 && path !== ''}
					<p class="secondary pad">{nothing}</p>
				{/if}
			{/if}
		</div>
		<div class="actions">
			<NeoButton text onclick={() => (open = false)}>Cancel</NeoButton>
			{#if picksDirectory}
				<NeoButton class="primary" elevation={2} disabled={path === ''} onclick={() => choose(path)}>Choose</NeoButton>
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
	.bar {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
		align-items: center;
	}
	.list {
		position: relative;
		display: flex;
		flex-direction: column;
		height: 50vh;
		overflow: auto;
		transition: opacity var(--nebu-normal) var(--nebu-settle);
	}
	.list.busy {
		opacity: 0.6;
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
