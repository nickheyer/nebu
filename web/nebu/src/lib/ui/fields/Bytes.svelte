<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import Stepper from '#lib/ui/fields/Stepper.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';

	type Unit = 'MB' | 'GB' | 'TB';
	const units: Unit[] = ['MB', 'GB', 'TB'];
	const factors: Record<Unit, number> = { MB: 1e6, GB: 1e9, TB: 1e12 };

	// A byte count as a number and a decimal unit, the empty box showing the count it resolves to
	let {
		value = $bindable(0),
		label,
		info,
		disabled = false,
		message,
		fallback
	}: { value?: number; label?: string; info?: string; disabled?: boolean; message?: string; fallback?: number } = $props();

	const id = fieldId();
	let unit = $state<Unit>('GB');
	let raw = $state<number | string | null>(0);
	let focused = $state(false);
	let emitted: number | undefined;

	// Two decimals in the given unit
	function inUnit(n: number, u: Unit): number {
		return Math.round((n / factors[u]) * 100) / 100;
	}

	// A value set from outside takes the largest unit it reaches
	$effect(() => {
		if (focused || value === emitted) return;
		let best: Unit = 'MB';
		if (value >= factors.TB) best = 'TB';
		else if (value >= factors.GB) best = 'GB';
		unit = best;
		raw = inUnit(value, best);
	});

	function changed() {
		const n = Number(raw);
		if (raw === '' || raw === null || Number.isNaN(n)) return;
		emitted = Math.round(n * factors[unit]);
		value = emitted;
	}

	function pickUnit(u: Unit) {
		if (disabled) return;
		unit = u;
		changed();
	}
</script>

{#snippet unitButtons()}
	{#each units as u (u)}
		<NeoButton text toggle checked={unit === u} color={unit === u ? 'primary' : undefined} role="radio" aria-checked={unit === u} {disabled} onclick={() => pickUnit(u)}>{u}</NeoButton>
	{/each}
{/snippet}

<div class="field">
	<FieldLabel {label} {info} forId={id} />
	<Stepper {id} bind:raw bind:focused min={0} step={unit === 'MB' ? 1 : 0.5} {disabled} {message} fallback={fallback === undefined ? undefined : inUnit(fallback, unit)} ariaLabel={label} tail={unitButtons} onchange={changed} />
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
</style>
