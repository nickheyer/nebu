<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoPopConfirm } from '#lib/neo/floating/tooltips/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, num } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Bar, { type Segment } from '#lib/ui/Bar.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Tip, { type Fact } from '#lib/ui/Tip.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import Row from '#lib/ui/layout/Row.svelte';

	const store = $derived(live.store);
	const used = $derived(store ? Number(store.blobBytes + store.partialBytes + store.cacheBytes) : 0);
	const limit = $derived(store ? Number(store.maxBytes) : 0);
	const over = $derived(limit > 0 && used > limit);
	// The filesystem holding the store bounds the bar when no limit is set
	const disk = $derived(store ? live.host?.storage.find((s) => s.uses.includes(store.path)) : undefined);
	const cap = $derived(limit > 0 ? Math.max(limit, used) : disk ? Number(disk.totalBytes) : used);
	const facts = $derived<Fact[]>(
		store
			? [
					{ label: 'models', value: `${num(store.models)}, ${bytes(store.blobBytes)}` },
					{ label: 'partials', value: `${num(store.partials)}, ${bytes(store.partialBytes)}` },
					{ label: 'caches', value: `${num(store.caches)}, ${bytes(store.cacheBytes)}` },
					{ label: 'limit', value: limit > 0 ? bytes(limit) : 'none' },
					{ label: 'disk', value: disk ? `${bytes(disk.freeBytes)} free of ${bytes(disk.totalBytes)}` : 'unknown' }
				]
			: []
	);
	// Models, partials and caches against the limit, all in the warning colour once past it
	const segments = $derived<Segment[]>(
		over
			? [{ kind: 'warning', label: 'used', bytes: used }]
			: [
					{ kind: 'used', label: 'models', bytes: Number(store?.blobBytes ?? 0) },
					{ kind: 'reservation', label: 'partials', bytes: Number(store?.partialBytes ?? 0) },
					{ kind: 'other', label: 'caches', bytes: Number(store?.cacheBytes ?? 0) }
				]
	);
	let partials = $state(false);
	let open = $state(false);

	async function collect() {
		try {
			const res = await rpc.store.gc({ partials });
			toast.success('Collected', `${num(res.removed)} blobs, ${bytes(res.freedBytes)} freed`);
		} catch (err) {
			toast.error('Collect failed', reason(err));
		}
	}
</script>

{#if !live.ready}
	<Skeleton rows={1} height={36} />
{:else}
	<Row gap={16} wrap={false}>
		<Cell grow={false}>
			<Tip {facts} tag="span">
				<span class="used" class:warning-text={over}>{limit > 0 ? `${bytes(used)} of ${bytes(limit)}` : `${bytes(used)}, no limit`}</span>
			</Tip>
		</Cell>
		<Cell min="6rem">
			<Tip {facts} tag="div" class="store-track">
				<Bar {segments} total={cap} facts={false} />
			</Tip>
		</Cell>
		<Cell grow={false}>
			<NeoPopConfirm bind:open elevation={2} tooltipProps={{ placement: 'bottom-end' }} header="Collect unreferenced blobs" onConfirm={collect} confirmProps={{ label: 'Collect' }}>
				{#snippet tooltip()}
					<div class="confirm"><Switch bind:checked={partials} label="Partial downloads" /></div>
				{/snippet}
				<NeoButton elevation={2} onclick={() => (open = true)}>Collect</NeoButton>
			</NeoPopConfirm>
		</Cell>
	</Row>
{/if}

<style>
	.used {
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
	:global(.store-track) {
		display: flex;
		flex: 1 1 auto;
		align-items: center;
		min-height: 36px;
	}
	.confirm {
		padding: 4px 4px 8px;
	}
</style>
