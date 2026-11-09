<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import Stepper from '#lib/ui/fields/Stepper.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';

	type Unit = 'MB' | 'GB' | 'TB';
	const units: Unit[] = ['MB', 'GB', 'TB'];
	// A typed size is decimal as its unit reads, memory against a device total follows the pool sizes in binary steps
	const decimal: Record<Unit, number> = { MB: 1e6, GB: 1e9, TB: 1e12 };
	const binary: Record<Unit, number> = { MB: 1 << 20, GB: 1 << 30, TB: 2 ** 40 };

	let {
		value = $bindable(0),
		total,
		label,
		info,
		disabled = false,
		message,
		blank = false,
		// The byte count an unset value resolves to
		fallback
	}: { value?: number; total?: number; label?: string; info?: string; disabled?: boolean; message?: string; blank?: boolean; fallback?: number } = $props();

	const id = fieldId();
	const factors = $derived(total === undefined ? decimal : binary);
	let unit = $state<Unit>('GB');
	let raw = $state<number | string | null>(0);
	let focused = $state(false);
	let gb = $state(0);
	let emitted: number | undefined;

	// Two decimals in the given unit
	function inUnit(n: number, u: Unit): number {
		return Math.round((n / factors[u]) * 100) / 100;
	}

	// A value set from outside takes the largest unit it reaches
	$effect(() => {
		if (total !== undefined) {
			gb = inUnit(value, 'GB');
			return;
		}
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

	const totalGb = $derived(total === undefined ? 0 : total / factors.GB);
	const fallbackGb = $derived(fallback === undefined ? undefined : Math.round((fallback / factors.GB) * 10) / 10);
</script>

{#snippet unitButtons()}
	{#each units as u (u)}
		<NeoButton text toggle checked={unit === u} color={unit === u ? 'primary' : undefined} role="radio" aria-checked={unit === u} {disabled} onclick={() => pickUnit(u)}>{u}</NeoButton>
	{/each}
{/snippet}

{#if total !== undefined}
	<Slider value={gb} min={0} max={Math.ceil(totalGb * 10) / 10} step={0.5} unit="GB" {label} {info} {disabled} {message} {blank} fallback={fallbackGb} onchange={(n) => (value = Math.round(n * factors.GB))} />
{:else}
	<div class="field">
		<FieldLabel {label} {info} forId={id} />
		<Stepper {id} bind:raw bind:focused min={0} step={unit === 'MB' ? 1 : 0.5} {disabled} {message} placeholder={fallback === undefined ? '' : String(inUnit(fallback, unit))} ariaLabel={label} tail={unitButtons} onchange={changed} />
	</div>
{/if}

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
</style>
