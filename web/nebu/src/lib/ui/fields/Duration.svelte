<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import Stepper from '#lib/ui/fields/Stepper.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';

	type Unit = 'ms' | 's' | 'min' | 'h' | 'd';
	const factors: Record<Unit, number> = { ms: 1, s: 1000, min: 60_000, h: 3_600_000, d: 86_400_000 };

	// A span as a number and a unit, zero empty under emptyZero, the empty box showing its resolved span
	let {
		value = $bindable(0),
		base = 'ms',
		smallest = base,
		max,
		label,
		info,
		disabled = false,
		message,
		emptyZero = false,
		fallback
	}: { value?: number; base?: 'ms' | 's'; smallest?: Unit; max?: number; label?: string; info?: string; disabled?: boolean; message?: string; emptyZero?: boolean; fallback?: number } = $props();

	const id = fieldId();
	const baseFactor = $derived(base === 'ms' ? 1 : 1000);
	const units = $derived.by((): Unit[] => {
		const all: Unit[] = ['ms', 's', 'min', 'h', 'd'];
		const first = Math.max(factors[smallest], baseFactor);
		const limit = max === undefined ? Infinity : max * baseFactor;
		return all.filter((u) => factors[u] >= first && factors[u] <= Math.max(limit, first));
	});
	const defaultUnit = $derived<Unit>(units.includes('s') ? 's' : (units[0] ?? 'ms'));
	let unit = $state<Unit | undefined>(undefined);
	let raw = $state<number | string | null>('');
	let focused = $state(false);
	let emitted: number | undefined;

	// The largest unit dividing a span evenly
	function fit(ms: number): Unit {
		let best: Unit = units[0] ?? 'ms';
		for (const u of units) if (ms % factors[u] === 0) best = u;
		return best;
	}

	// A value set from outside takes the unit fitting it, an empty box the unit of its resolved span
	$effect(() => {
		const ms = value * baseFactor;
		if (focused || value === emitted) return;
		if (ms === 0) {
			raw = emptyZero ? '' : 0;
			unit = fallback ? fit(fallback * baseFactor) : defaultUnit;
			return;
		}
		unit = fit(ms);
		raw = ms / factors[unit];
	});

	function changed() {
		const blank = raw === '' || raw === null || raw === undefined;
		const n = blank ? 0 : Number(raw);
		if ((blank && !emptyZero) || Number.isNaN(n)) return;
		unit ??= defaultUnit;
		const limit = max === undefined ? Infinity : max * baseFactor;
		emitted = Math.min(limit, Math.max(0, n * factors[unit])) / baseFactor;
		value = emitted;
	}

	function pickUnit(u: Unit) {
		if (disabled) return;
		unit = u;
		changed();
	}

	const unitMax = $derived(max === undefined ? undefined : Math.floor((max * baseFactor) / factors[unit ?? defaultUnit]));
	const resolved = $derived(fallback ? Math.round(((fallback * baseFactor) / factors[unit ?? fit(fallback * baseFactor)]) * 100) / 100 : undefined);
</script>

{#snippet unitButtons()}
	{#each units as u (u)}
		<NeoButton text toggle checked={unit === u} color={unit === u ? 'primary' : undefined} role="radio" aria-checked={unit === u} {disabled} onclick={() => pickUnit(u)}>{u}</NeoButton>
	{/each}
{/snippet}

<div class="field">
	<FieldLabel {label} {info} forId={id} />
	<Stepper {id} bind:raw bind:focused min={0} max={unitMax} fallback={emptyZero ? resolved : undefined} {disabled} {message} ariaLabel={label} tail={unitButtons} onchange={changed} />
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
</style>
