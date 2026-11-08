<script lang="ts">
	import type { Segment } from '#lib/ui/Bar.svelte';

	type Kind = Segment['kind'];

	// Names the fills the bars actually show. Kinds with no bytes anywhere are left out so the legend never lies.
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
			<span class="item"><span class="swatch {item.kind}" aria-hidden="true"></span>{item.label}</span>
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
	.swatch {
		display: inline-block;
		width: 14px;
		height: 8px;
		border-radius: 2px;
	}
	.instance,
	.used {
		background: var(--neo-color-primary);
	}
	.reservation {
		background: repeating-linear-gradient(135deg, var(--neo-color-primary) 0 3px, transparent 3px 6px);
	}
	.planned {
		background: repeating-linear-gradient(135deg, var(--nebu-steel) 0 3px, transparent 3px 6px);
	}
	.other {
		background: var(--nebu-text-muted);
	}
	.warning {
		background: var(--neo-color-warning);
	}
	.free {
		background: var(--neo-background-color-secondary);
		box-shadow: var(--neo-box-shadow-inset-1);
	}
</style>
