<script lang="ts">
	export type Share = { label: string; value: number };

	// One bar cut into shares, each a step lighter than the last, every share named with its amount underneath
	let { shares, format = (n: number) => String(n) }: { shares: Share[]; format?: (n: number) => string } = $props();

	const shown = $derived(shares.filter((s) => s.value > 0));
	const total = $derived(shown.reduce((n, s) => n + s.value, 0));

	function tone(i: number): string {
		const strength = shown.length < 2 ? 100 : Math.round(100 - (i / (shown.length - 1)) * 72);
		return `color-mix(in srgb, var(--neo-color-primary) ${strength}%, var(--neo-background-color-secondary))`;
	}
</script>

<div class="split">
	<div class="track" role="img" aria-label={shown.map((s) => `${s.label} ${format(s.value)}`).join(', ')}>
		{#each shown as s, i (s.label)}
			<span class="seg" style:flex-grow={s.value / total} style:background={tone(i)}></span>
		{/each}
	</div>
	<div class="legend">
		{#each shown as s, i (s.label)}
			<span class="item"><span class="swatch" style:background={tone(i)} aria-hidden="true"></span>{s.label}<span class="amount">{format(s.value)}</span></span>
		{/each}
	</div>
</div>

<style>
	.split {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
	}
	.track {
		display: flex;
		gap: 2px;
		height: 10px;
		overflow: hidden;
		border-radius: 999px;
	}
	.seg {
		flex-basis: 0;
		min-width: 3px;
		height: 100%;
		transform-origin: 0 50%;
		animation: nebu-grow var(--nebu-slow) var(--nebu-settle) both;
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
	}
	.swatch {
		display: inline-block;
		width: 14px;
		height: 8px;
		border-radius: 2px;
	}
	.amount {
		font-variant-numeric: tabular-nums;
		color: var(--neo-text-color);
	}
</style>
