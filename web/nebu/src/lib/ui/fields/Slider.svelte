<script lang="ts">
	import { untrack } from 'svelte';
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
		// Whether the setting is unset, the box then empty and showing the placeholder
		blank = false,
		placeholder,
		onchange
	}: { value?: number; min?: number; max?: number; step?: number; unit?: string; label?: string; info?: string; disabled?: boolean; message?: string; blank?: boolean; placeholder?: string; onchange?: (n: number) => void } = $props();

	let typed = $state(untrack(() => (blank ? '' : String(value))));

	watch(
		() => {
			onchange?.(value);
		},
		() => value,
		{ skip: 1 }
	);

	$effect(() => {
		typed = blank ? '' : String(value);
	});

	function commit() {
		if (typed === '' && blank) return;
		const n = Number(typed);
		if (typed === '' || Number.isNaN(n)) {
			typed = blank ? '' : String(value);
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
			<NeoRange bind:value {min} {max} {step} {disabled} elevation={-1} tooltips={false} width="100%" />
		</div>
		<div class="box">
			<NeoInput type="text" inputmode="decimal" bind:value={typed} {disabled} {placeholder} elevation={-1} floating={false} width="100%" onchange={commit} onblur={commit} aria-label={label} />
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
		gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
	}
	.range {
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
	}
	.range :global(.neo-range-container) {
		min-width: 0;
	}
	.box {
		display: flex;
		flex: 0 0 9ch;
		min-width: 0;
	}
	.box :global(.neo-input-group) {
		min-width: 0;
	}
	.box :global(input) {
		text-align: right;
	}
	.unit {
		flex: 0 0 auto;
	}
</style>
