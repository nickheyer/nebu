<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Bar, { type Segment } from '#lib/ui/Bar.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import RecycleIcon from 'phosphor-svelte/lib/RecycleIcon';

	const store = $derived(live.store);
	const used = $derived(store ? Number(store.blobBytes + store.partialBytes + store.cacheBytes) : 0);
	const limit = $derived(store ? Number(store.maxBytes) : 0);
	// The filesystem holding the store bounds the bar when no limit is set
	const disk = $derived(store ? live.host?.storage.find((s) => s.uses.includes('store')) : undefined);
	const total = $derived(limit > 0 ? Math.max(limit, used) : disk ? Number(disk.totalBytes) : used);
	const reading = $derived(limit > 0 ? `${bytes(used)} of ${bytes(limit)}` : disk ? `${bytes(used)}, ${bytes(disk.freeBytes)} free` : bytes(used));

	// The store's files, unfinished downloads and caches, then the rest of the disk, all in warning colour past the limit
	const segments = $derived.by((): Segment[] => {
		if (!store) return [];
		if (limit > 0 && used > limit) return [{ kind: 'warning', label: 'over the limit', bytes: used }];
		const own: Segment[] = [
			{ kind: 'used', label: 'models', bytes: Number(store.blobBytes) },
			{ kind: 'reservation', label: 'unfinished downloads', bytes: Number(store.partialBytes) },
			{ kind: 'planned', label: 'caches', bytes: Number(store.cacheBytes) }
		];
		if (limit > 0) return [...own, { kind: 'free', label: 'free', bytes: limit - used }];
		if (!disk) return own;
		return [...own, { kind: 'other', label: 'other files', bytes: Math.max(0, Number(disk.totalBytes - disk.freeBytes) - used) }, { kind: 'free', label: 'free', bytes: Number(disk.freeBytes) }];
	});

	let cleaning = $state(false);

	// Deletes the files no model uses
	async function collect() {
		cleaning = true;
		try {
			const res = await rpc.store.gc({});
			toast.success('Cleaned up', `${plural(Number(res.removed), 'file')}, ${bytes(res.freedBytes)} freed`);
		} catch (err) {
			toast.error('Clean up failed', reason(err));
		} finally {
			cleaning = false;
		}
	}
</script>

<Card title="Store">
	{#snippet tools()}
		<NeoButton text aria-label="Clean up" loading={cleaning} onclick={collect}>{#snippet icon()}<RecycleIcon size={20} />{/snippet}</NeoButton>
	{/snippet}
	{#if !store}
		<Skeleton rows={1} kind="bars" />
	{:else}
		<div class="gauge">
			<span class="track"><Bar {segments} {total} format={(n) => bytes(n)} /></span>
			<span class="reading">{reading}</span>
		</div>
	{/if}
</Card>

<style>
	.gauge {
		display: flex;
		gap: var(--nebu-space-4);
		align-items: center;
		min-width: 0;
	}
	.track {
		display: flex;
		flex: 1 1 auto;
		min-width: 4rem;
	}
	.reading {
		flex: 0 0 auto;
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
</style>
