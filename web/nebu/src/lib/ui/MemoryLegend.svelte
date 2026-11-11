<script lang="ts">
	import type { Segment } from '#lib/ui/Bar.svelte';
	import Swatch from '#lib/ui/Swatch.svelte';

	type Kind = Segment['kind'];

	// Names only the fills the bars show
	let { kinds }: { kinds: Iterable<Kind> } = $props();

	const order: { kind: Kind; label: string }[] = [
		{ kind: 'instance', label: 'Instances' },
		{ kind: 'used', label: 'Used' },
		{ kind: 'reservation', label: 'Reserved' },
		{ kind: 'planned', label: 'Planned' },
		{ kind: 'other', label: 'Other' },
		{ kind: 'warning', label: 'Over' },
		{ kind: 'free', label: 'Free' }
	];
	const items = $derived.by(() => {
		const present = new Set(kinds);
		return order.filter((o) => present.has(o.kind));
	});
</script>

{#if items.length > 1}
	<div class="legend" aria-label="Legend">
		{#each items as item (item.kind)}
			<span class="item"><Swatch kind={item.kind} />{item.label}</span>
		{/each}
	</div>
{/if}

<style>
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
	}
</style>
