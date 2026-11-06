<script lang="ts">
	import { bytes } from '#lib/format.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';

	type Unit = 'MB' | 'GB' | 'TB';
	const factors: Record<Unit, number> = { MB: 1 << 20, GB: 1 << 30, TB: 2 ** 40 };

	let {
		value = $bindable(0),
		// The known total the value is a share of, a range when set
		total,
		label,
		info,
		disabled = false,
		message
	}: { value?: number; total?: number; label?: string; info?: string; disabled?: boolean; message?: string } = $props();

	let unit = $state<Unit>('GB');
	let shown = $state(0);
	let gb = $state(0);

	$effect(() => {
		if (total !== undefined) {
			gb = value / factors.GB;
			return;
		}
		let best: Unit = 'MB';
		if (value >= factors.TB && value % factors.TB === 0) best = 'TB';
		else if (value >= factors.GB && value % factors.GB === 0) best = 'GB';
		unit = best;
		shown = value / factors[best];
	});

	const totalGb = $derived(total === undefined ? 0 : total / factors.GB);
</script>

{#if total !== undefined}
	<Slider value={gb} min={0} max={Math.ceil(totalGb * 10) / 10} step={0.5} unit="GB" {label} {info} {disabled} message={message ?? `${bytes(value)} of ${bytes(total)}`} onchange={(n) => (value = Math.round(n * factors.GB))} />
{:else}
	<div class="field">
		<FieldLabel {label} {info} />
		<div class="controls">
			<NumberField value={shown} min={0} {disabled} {message} onchange={(n) => (value = Math.round(n * factors[unit]))} />
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
		flex-wrap: wrap;
		gap: 8px;
		align-items: center;
	}
</style>
