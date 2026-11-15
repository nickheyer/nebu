<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Bar, { type Segment } from '#lib/ui/Bar.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Swatch from '#lib/ui/Swatch.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import RecycleIcon from 'phosphor-svelte/lib/RecycleIcon';

	const store = $derived(live.store);
	const files = $derived(store ? Number(store.blobBytes + store.partialBytes + store.cacheBytes) : 0);
	const limit = $derived(store ? Number(store.maxBytes) : 0);
	// The filesystem holding the store bounds the bar when no limit is set
	const disk = $derived(store ? live.host?.storage.find((s) => s.uses.includes('store')) : undefined);
	// What the store takes on its disk as the host measures it, the figure the Host page shows for it
	const measured = $derived(Object.entries(disk?.useBytes ?? {}).find(([k]) => k.split(', ').includes('store'))?.[1]);
	const used = $derived(Math.max(files, Number(measured ?? 0n)));
	const total = $derived(limit > 0 ? Math.max(limit, used) : disk ? Number(disk.totalBytes) : used);
	const reading = $derived(limit > 0 ? `${bytes(used)} of ${bytes(limit)}` : bytes(used));

	// The store's models, unfinished downloads, caches with its index, then the rest of the disk, all in warning colour past the limit
	const parts = $derived.by((): Segment[] => {
		if (!store) return [];
		if (limit > 0 && used > limit) return [{ kind: 'warning', label: 'Over the limit', bytes: used }];
		const own: Segment[] = [
			{ kind: 'used', label: 'Models', bytes: Number(store.blobBytes) },
			{ kind: 'reservation', label: 'Unfinished downloads', bytes: Number(store.partialBytes) },
			{ kind: 'planned', label: 'Caches and index', bytes: Number(store.cacheBytes) + (used - files) }
		];
		if (limit > 0) return [...own, { kind: 'free', label: 'Left under the limit', bytes: limit - used }];
		if (!disk) return own;
		return [...own, { kind: 'other', label: 'Other files on the disk', bytes: Math.max(0, Number(disk.totalBytes - disk.freeBytes) - used) }, { kind: 'free', label: 'Free on the disk', bytes: Number(disk.freeBytes) }];
	});
	const segments = $derived(parts);
	// Every fill the bar can show with what it holds, so the bar reads without a pointer over it
	const legend = $derived(parts.filter((p) => p.bytes > 0 || p.kind === 'used'));

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
		<NeoButton text loading={cleaning} onclick={collect}>{#snippet icon()}<RecycleIcon size={16} />{/snippet}Clean up</NeoButton>
	{/snippet}
	{#if !store}
		<Skeleton rows={1} kind="bars" />
	{:else}
		<div class="gauge">
			<span class="track"><Bar {segments} {total} format={(n) => bytes(n)} /></span>
			<span class="reading">{reading}</span>
		</div>
		<div class="legend" aria-label="Legend">
			{#each legend as p (p.label)}
				<span class="item"><Swatch kind={p.kind} />{p.label}<span class="held">{bytes(p.bytes)}</span></span>
			{/each}
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
	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-1) var(--nebu-space-4);
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-muted);
	}
	.item {
		display: inline-flex;
		gap: 6px;
		align-items: center;
		white-space: nowrap;
	}
	.held {
		font-variant-numeric: tabular-nums;
		color: var(--neo-text-color);
	}
	.reading {
		flex: 0 0 auto;
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
</style>
