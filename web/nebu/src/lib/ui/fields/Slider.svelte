<script lang="ts">
	import { untrack } from 'svelte';
	import { NeoInput, NeoRange } from '#lib/neo/inputs/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';

	// A bounded number as a rail and a box, an unset value resting on the number it resolves to
	let {
		value = $bindable(),
		min = 0,
		max = 100,
		step = 1,
		unit,
		label,
		info,
		disabled = false,
		message,
		fallback,
		optional = false,
		onchange
	}: {
		value?: number;
		min?: number;
		max?: number;
		step?: number;
		unit?: string;
		label?: string;
		info?: string;
		disabled?: boolean;
		message?: string;
		fallback?: number;
		optional?: boolean;
		onchange?: (n: number | undefined) => void;
	} = $props();

	const id = fieldId();
	const rest = $derived(value ?? fallback ?? min);
	const shown = $derived(value === undefined ? '' : String(value));
	let typed = $state(untrack(() => shown));
	let knob = $state(untrack(() => rest));
	const blank = $derived(value === undefined);
	const unset = $derived(blank && fallback === undefined);

	// The box and the knob follow the value
	$effect(() => {
		typed = shown;
		knob = rest;
	});

	// A dragged knob becomes the value
	$effect(() => {
		const k = knob;
		untrack(() => {
			if (k === rest) return;
			value = k;
			onchange?.(k);
		});
	});

	// Takes the typed number inside the bounds, an emptied box unsetting an optional value
	function commit() {
		if (typed === shown) return;
		if (typed.trim() === '') {
			if (!optional) {
				typed = shown;
				return;
			}
			value = undefined;
			onchange?.(undefined);
			return;
		}
		const n = Number(typed);
		if (Number.isNaN(n)) {
			typed = shown;
			return;
		}
		const next = Math.min(max, Math.max(min, n));
		typed = String(next);
		value = next;
		onchange?.(next);
	}
</script>

{#snippet after()}
	<span class="unit">{unit}</span>
{/snippet}

<div class="field">
	<FieldLabel {label} {info} forId={id} />
	<div class="controls" class:blank class:unset>
		<div class="range">
			<NeoRange bind:value={knob} {min} {max} {step} {disabled} elevation={-1} tooltips={false} width="100%" />
		</div>
		<div class="box" style:flex-basis={unit ? `calc(7rem + ${unit.length}ch)` : '5rem'}>
			<NeoInput type="text" inputmode="decimal" {id} bind:value={typed} {disabled} placeholder={fallback === undefined ? undefined : String(fallback)} elevation={-1} floating={false} width="100%" after={unit ? after : undefined} onchange={commit} onblur={commit} aria-label={label} />
		</div>
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
		transition: opacity var(--nebu-normal) var(--nebu-ease);
	}
	.range :global(.neo-range-container) {
		min-width: 0;
	}
	.blank .range {
		opacity: 0.55;
	}
	.unset .range :global(.neo-range-handle) {
		opacity: 0;
	}
	.unset .range :global(.neo-range-handle-before) {
		background-color: transparent;
	}
	/* The box is as wide as a few digits and its unit need, the rail taking the rest */
	.box {
		display: flex;
		flex: 0 0 5rem;
		min-width: 0;
	}
	.box :global(.neo-input-group) {
		flex: 1 1 auto;
		min-width: 0;
	}
	.box :global(input) {
		text-align: right;
	}
	.unit {
		padding: 0 8px 0 4px;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
</style>
