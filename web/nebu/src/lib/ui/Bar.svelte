<script lang="ts">
	import Tip from '#lib/ui/Tip.svelte';

	export type Segment = { kind: 'instance' | 'reservation' | 'other' | 'free' | 'used' | 'planned' | 'warning'; label: string; bytes: number };

	let {
		segments,
		total,
		height = 10,
		facts = true,
		format = (n: number) => String(n)
	}: { segments: Segment[]; total: number; height?: 6 | 8 | 10 | 12; facts?: boolean; format?: (n: number) => string } = $props();

	const shown = $derived(segments.filter((s) => s.kind !== 'free'));
	const widths = $derived(shown.map((s) => (total > 0 ? Math.min(100, (s.bytes / total) * 100) : 0)));
	const tips = $derived(segments.filter((s) => s.bytes > 0).map((s) => ({ label: s.label, value: format(s.bytes) })));
</script>

<Tip facts={facts ? tips : undefined} tag="div" class="bar-tip">
	<div class="track" style:height="{height}px">
		{#each shown as s, i (i)}
			<span class="seg {s.kind}" style:width="{widths[i]}%"></span>
		{/each}
	</div>
</Tip>

<style>
	:global(.bar-tip) {
		display: flex;
		flex: 1 1 auto;
		width: 100%;
	}
	.track {
		display: flex;
		flex: 1 1 auto;
		overflow: hidden;
		background: var(--neo-background-color-secondary);
		border-radius: 999px;
		box-shadow: var(--neo-box-shadow-inset-1);
	}
	.seg {
		display: block;
		flex: 0 0 auto;
		height: 100%;
		transform-origin: 0 50%;
		animation: nebu-grow var(--nebu-slow) var(--nebu-settle) both;
		transition: width var(--nebu-slow) var(--nebu-settle);
	}
	.instance,
	.used {
		background: var(--neo-color-primary);
	}
	.instance + .instance {
		border-left: 1px solid var(--neo-background-color);
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
</style>
