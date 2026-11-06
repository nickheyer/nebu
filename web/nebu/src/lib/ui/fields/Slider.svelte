<script lang="ts">
	import { watch } from '@dvcol/svelte-utils/watch';
	import { NeoInput, NeoRange } from '#lib/neo/inputs/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';

	let {
		value = $bindable(0),
		min = 0,
		max = 100,
		step = 1,
		unit,
		label,
		info,
		disabled = false,
		message,
		onchange
	}: { value?: number; min?: number; max?: number; step?: number; unit?: string; label?: string; info?: string; disabled?: boolean; message?: string; onchange?: (n: number) => void } = $props();

	let typed = $state(String(value));

	watch(
		() => {
			onchange?.(value);
		},
		() => value,
		{ skip: 1 }
	);

	$effect(() => {
		typed = String(value);
	});

	function commit() {
		const n = Number(typed);
		if (Number.isNaN(n)) {
			typed = String(value);
			return;
		}
		value = Math.min(max, Math.max(min, n));
		typed = String(value);
	}
</script>

<div class="field">
	<FieldLabel {label} {info} />
	<div class="controls">
		<div class="range">
			<NeoRange bind:value {min} {max} {step} {disabled} elevation={-1} tooltips={false} />
		</div>
		<div class="box">
			<NeoInput type="number" bind:value={typed} {min} {max} {step} {disabled} elevation={-1} floating={false} onchange={commit} onblur={commit} aria-label={label} />
		</div>
		{#if unit}<span class="unit secondary">{unit}</span>{/if}
	</div>
	{#if message}<span class="secondary">{message}</span>{/if}
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
		gap: 12px;
		align-items: center;
	}
	.range {
		flex: 1 1 auto;
		min-width: 0;
	}
	.box {
		flex: 0 0 9ch;
	}
	.unit {
		flex: 0 0 auto;
	}
</style>
