<script lang="ts">
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';

	type Unit = 'ms' | 's' | 'min' | 'h' | 'd';
	const factors: Record<Unit, number> = { ms: 1, s: 1000, min: 60_000, h: 3_600_000, d: 86_400_000 };

	let {
		value = $bindable(0),
		// Whether the bound value counts milliseconds or seconds
		base = 'ms',
		max,
		label,
		info,
		placeholder,
		disabled = false,
		message
	}: { value?: number; base?: 'ms' | 's'; max?: number; label?: string; info?: string; placeholder?: string; disabled?: boolean; message?: string } = $props();

	const baseFactor = $derived(base === 'ms' ? 1 : 1000);
	const units = $derived.by((): Unit[] => {
		const all: Unit[] = base === 'ms' ? ['ms', 's', 'min', 'h', 'd'] : ['s', 'min', 'h', 'd'];
		const limit = max === undefined ? Infinity : max * baseFactor;
		return all.filter((u) => factors[u] <= Math.max(limit, factors[all[0]!]));
	});
	let unit = $state<Unit>('ms');
	let shown = $state(0);

	// Picks the largest unit that divides the value and shows it
	$effect(() => {
		const ms = value * baseFactor;
		if (ms === 0) {
			shown = 0;
			return;
		}
		let best: Unit = units[0] ?? base;
		for (const u of units) if (ms % factors[u] === 0) best = u;
		unit = best;
		shown = ms / factors[best];
	});

	function changed(n: number) {
		const ms = n * factors[unit];
		const limit = max === undefined ? Infinity : max * baseFactor;
		value = Math.min(limit, Math.max(0, ms)) / baseFactor;
	}

	function unitChanged(u: Unit) {
		unit = u;
		changed(shown);
	}

	const unitOptions = $derived(units.map((u) => ({ value: u, label: u })));
	const unitMax = $derived(max === undefined ? undefined : Math.floor((max * baseFactor) / factors[unit]));
</script>

<div class="field">
	<FieldLabel {label} {info} />
	<div class="controls">
		<NumberField value={shown} min={0} max={unitMax} {placeholder} {disabled} {message} onchange={changed} />
		<Segmented bind:value={unit} options={unitOptions} {disabled} onchange={unitChanged} />
	</div>
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	/* The number takes the width the unit choice leaves, both on one line */
	.controls {
		display: flex;
		gap: 8px;
		align-items: center;
		min-width: 0;
	}
	.controls > :global(:first-child) {
		flex: 1 1 auto;
		min-width: 0;
	}
	.controls > :global(:last-child) {
		flex: 0 0 auto;
	}
</style>
