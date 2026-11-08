<script lang="ts">
	import type { Snippet } from 'svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';

	type Unit = 'ms' | 's' | 'min' | 'h' | 'd';
	const factors: Record<Unit, number> = { ms: 1, s: 1000, min: 60_000, h: 3_600_000, d: 86_400_000 };

	let {
		value = $bindable(0),
		base = 'ms',
		max,
		label,
		disabled = false,
		message,
		emptyZero = false,
		// The span an empty box resolves to, in the base unit
		fallback,
		mark
	}: { value?: number; base?: 'ms' | 's'; max?: number; label?: string; disabled?: boolean; message?: string; emptyZero?: boolean; fallback?: number; mark?: Snippet } = $props();

	const baseFactor = $derived(base === 'ms' ? 1 : 1000);
	const units = $derived.by((): Unit[] => {
		const all: Unit[] = base === 'ms' ? ['ms', 's', 'min', 'h', 'd'] : ['s', 'min', 'h', 'd'];
		const limit = max === undefined ? Infinity : max * baseFactor;
		return all.filter((u) => factors[u] <= Math.max(limit, factors[all[0]!]));
	});
	let unit = $state<Unit>('ms');
	let shown = $state(0);

	// The largest unit dividing a span evenly
	function fit(ms: number): Unit {
		let best: Unit = units[0] ?? base;
		if (ms === 0) return best;
		for (const u of units) if (ms % factors[u] === 0) best = u;
		return best;
	}

	// Shows the value in the unit that fits it, an empty box taking the fallback's unit
	$effect(() => {
		const ms = value * baseFactor;
		if (ms === 0) {
			shown = 0;
			if (emptyZero && fallback) unit = fit(fallback * baseFactor);
			return;
		}
		unit = fit(ms);
		shown = ms / factors[unit];
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
	const placeholder = $derived(emptyZero && fallback ? String(Math.round(((fallback * baseFactor) / factors[unit]) * 100) / 100) : undefined);
</script>

<div class="field">
	<FieldLabel {label} />
	<div class="controls">
		<NumberField value={shown} min={0} max={unitMax} {placeholder} {disabled} {message} {emptyZero} mark={emptyZero && !fallback ? mark : undefined} onchange={changed} />
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
	.controls {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
		align-items: center;
		min-width: 0;
	}
	.controls > :global(:first-child) {
		flex: 1 1 12rem;
		min-width: 12rem;
	}
	.controls > :global(:last-child) {
		flex: 0 0 auto;
	}
</style>
