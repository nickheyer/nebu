<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import Resting from '#lib/ui/fields/Resting.svelte';
	import Stepper from '#lib/ui/fields/Stepper.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';

	type Unit = 'ms' | 's' | 'min' | 'h' | 'd';
	const factors: Record<Unit, number> = { ms: 1, s: 1000, min: 60_000, h: 3_600_000, d: 86_400_000 };

	// A span as a number and one of the units that can hold it, zero empty under emptyZero with the span it comes to written under it
	let {
		value = $bindable(0),
		base = 'ms',
		smallest = base,
		// The largest unit offered, for a span no one states in hours or days
		largest = 'd',
		max,
		label,
		info,
		disabled = false,
		message,
		emptyZero = false,
		fallback,
		// What an empty box comes to in words, when no span says it
		unset
	}: { value?: number; base?: 'ms' | 's'; smallest?: Unit; largest?: Unit; max?: number; label?: string; info?: string; disabled?: boolean; message?: string; emptyZero?: boolean; fallback?: number; unset?: string } = $props();

	const id = fieldId();
	const baseFactor = $derived(base === 'ms' ? 1 : 1000);
	const units = $derived.by((): Unit[] => {
		const all: Unit[] = ['ms', 's', 'min', 'h', 'd'];
		const first = Math.max(factors[smallest], baseFactor);
		const limit = max === undefined ? Infinity : max * baseFactor;
		// A unit is offered while more than one of it fits under the most the span may be
		return all.filter((u) => factors[u] >= first && factors[u] <= factors[largest] && (factors[u] < limit || factors[u] === first));
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
	// The span an empty box comes to in the largest unit dividing it
	const rest = $derived.by(() => {
		if (!fallback) return unset;
		const u = fit(fallback * baseFactor);
		return `${Math.round(((fallback * baseFactor) / factors[u]) * 100) / 100} ${u}`;
	});
	const blank = $derived(raw === '' || raw === null || raw === undefined);
</script>

{#snippet unitButtons()}
	{#each units.length > 1 ? units : [] as u (u)}
		<NeoButton text toggle checked={unit === u} color={unit === u ? 'primary' : undefined} role="radio" aria-checked={unit === u} {disabled} onclick={() => pickUnit(u)}>{u}</NeoButton>
	{/each}
{/snippet}

<div class="field">
	<FieldLabel {label} {info} forId={id} />
	<Stepper {id} bind:raw bind:focused min={0} max={unitMax} fallback={emptyZero ? resolved : undefined} {disabled} {message} ariaLabel={label} unit={units.length > 1 ? undefined : units[0]} tail={units.length > 1 ? unitButtons : undefined} onchange={changed} />
	{#if emptyZero && blank && rest !== undefined}<Resting text={rest} />{/if}
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
</style>
