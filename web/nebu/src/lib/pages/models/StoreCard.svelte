<script lang="ts">
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, num, plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Bar, { type Segment } from '#lib/ui/Bar.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Facts, { type FactItem } from '#lib/ui/Facts.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Card from '#lib/ui/layout/Card.svelte';

	const store = $derived(live.store);
	const used = $derived(store ? Number(store.blobBytes + store.partialBytes + store.cacheBytes) : 0);
	const limit = $derived(store ? Number(store.maxBytes) : 0);
	const over = $derived(limit > 0 && used > limit);
	// The filesystem holding the store bounds the bar when no limit is set
	const disk = $derived(store ? live.host?.storage.find((s) => s.uses.includes('store')) : undefined);
	const cap = $derived(limit > 0 ? Math.max(limit, used) : disk ? Number(disk.totalBytes) : 0);

	// A count with its bytes, or None when the store holds nothing of the kind
	function share(count: bigint, n: bigint): string {
		return count > 0n ? `${num(count)} · ${bytes(n)}` : 'None';
	}

	const items = $derived.by((): FactItem[] => {
		if (!store) return [];
		return [
			{ id: 'used', label: 'Used', value: limit > 0 ? `${bytes(used)} of ${bytes(limit)}` : bytes(used), lamp: over ? 'warning' : undefined, facts: [{ label: 'limit', value: limit > 0 ? bytes(limit) : 'none' }] },
			{ id: 'models', label: 'Models', value: store.models > 0n ? num(store.models) : 'None' },
			{ id: 'blobs', label: 'Blobs', value: share(store.blobs, store.blobBytes), tip: 'Stored files, Collect frees the ones no model references' },
			{ id: 'partials', label: 'Partials', value: share(store.partials, store.partialBytes), tip: 'Downloads that stopped before they finished' },
			{ id: 'caches', label: 'Caches', value: share(store.caches, store.cacheBytes), tip: 'Tensor caches formation stages keep on disk' },
			{
				id: 'disk',
				label: 'Disk free',
				value: disk ? bytes(disk.freeBytes) : 'Unknown',
				facts: [
					{ label: 'path', value: store.path },
					...(disk ? [{ label: 'mount', value: disk.path }, { label: 'size', value: bytes(disk.totalBytes) }] : [])
				]
			}
		];
	});

	// Models, partials and caches against the limit or the disk, all in the warning colour once past the limit
	const segments = $derived<Segment[]>(
		over
			? [{ kind: 'warning', label: 'used', bytes: used }]
			: [
					{ kind: 'used', label: 'blobs', bytes: Number(store?.blobBytes ?? 0) },
					{ kind: 'reservation', label: 'partials', bytes: Number(store?.partialBytes ?? 0) },
					{ kind: 'other', label: 'caches', bytes: Number(store?.cacheBytes ?? 0) },
					{ kind: 'free', label: 'free', bytes: Math.max(0, cap - used) }
				]
	);

	let partials = $state(false);

	async function collect() {
		try {
			const res = await rpc.store.gc({ partials });
			toast.success('Collected', `${plural(Number(res.removed), 'blob')}, ${bytes(res.freedBytes)} freed`);
		} catch (err) {
			toast.error('Collect failed', reason(err));
		}
	}
</script>

<Card title="Store">
	{#snippet tools()}
		<Confirm label="Collect" question="Remove blobs no model references" onconfirm={collect}>
			{#snippet body()}<Switch bind:checked={partials} label="Partial downloads too" />{/snippet}
		</Confirm>
	{/snippet}
	{#if !store}
		<Skeleton rows={1} kind="strip" />
	{:else}
		<Facts {items} />
		{#if cap > 0}<Bar {segments} total={cap} format={(n) => bytes(n)} />{/if}
	{/if}
</Card>
