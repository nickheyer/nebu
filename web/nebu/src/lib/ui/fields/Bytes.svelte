<script lang="ts">
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';

	type Unit = 'MB' | 'GB' | 'TB';
	const factors: Record<Unit, number> = { MB: 1 << 20, GB: 1 << 30, TB: 2 ** 40 };

	let {
		value = $bindable(0),
		total,
		label,
		disabled = false,
		message,
		blank = false,
		// The byte count an unset value resolves to
		fallback
	}: { value?: number; total?: number; label?: string; disabled?: boolean; message?: string; blank?: boolean; fallback?: number } = $props();

	let unit = $state<Unit>('GB');
	let shown = $state(0);
	let gb = $state(0);

	// Two decimals in the given unit
	function inUnit(n: number, u: Unit): number {
		return Math.round((n / factors[u]) * 100) / 100;
	}

	$effect(() => {
		if (total !== undefined) {
			gb = inUnit(value, 'GB');
			return;
		}
		let best: Unit = 'MB';
		if (value >= factors.TB) best = 'TB';
		else if (value >= factors.GB) best = 'GB';
		unit = best;
		shown = inUnit(value, best);
	});

	const totalGb = $derived(total === undefined ? 0 : total / factors.GB);
	const fallbackGb = $derived(fallback === undefined ? undefined : Math.round((fallback / factors.GB) * 10) / 10);
</script>

{#if total !== undefined}
	<Slider value={gb} min={0} max={Math.ceil(totalGb * 10) / 10} step={0.5} unit="GB" {label} {disabled} {message} {blank} fallback={fallbackGb} onchange={(n) => (value = Math.round(n * factors.GB))} />
{:else}
	<div class="field">
		<FieldLabel {label} />
		<div class="controls">
			<NumberField value={shown} min={0} step={unit === 'MB' ? 1 : 0.5} {disabled} {message} placeholder={fallback === undefined ? undefined : String(inUnit(fallback, unit))} onchange={(n) => (value = Math.round(n * factors[unit]))} />
			<Segmented bind:value={unit} options={[{ value: 'MB', label: 'MB' }, { value: 'GB', label: 'GB' }, { value: 'TB', label: 'TB' }]} {disabled} onchange={() => (value = Math.round(shown * factors[unit]))} />
		</div>
	</div>
{/if}

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
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
