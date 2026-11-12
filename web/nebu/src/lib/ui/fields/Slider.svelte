<script lang="ts">
	import { untrack } from 'svelte';
	import { NeoInput, NeoRange } from '#lib/neo/inputs/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';

	// A bounded number as a rail and a box, an unset value leaving both empty
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
		optional = false,
		// The label beside a short box at the row's end and the rail under both, for a narrow column
		inline = false,
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
		optional?: boolean;
		inline?: boolean;
		onchange?: (n: number | undefined) => void;
	} = $props();

	const id = fieldId();
	// Where the knob sits, the value held inside the rail, the rail's start while unset
	const rest = $derived(Math.min(max, Math.max(min, value ?? min)));
	const shown = $derived(value === undefined ? '' : String(value));
	let typed = $state(untrack(() => shown));
	let knob = $state(untrack(() => rest));
	// Whether a pointer or a key is on the rail
	let held = $state(false);
	const blank = $derived(value === undefined);
	// Characters of the longest number the box shows, a bound, the value, or the maximum at the step's decimals
	const decimals = $derived((String(step).split('.')[1] ?? '').length);
	const chars = $derived(Math.max(...[min, max, value].filter((n) => n !== undefined).map((n) => String(n).length), String(Math.trunc(max)).length + (decimals ? decimals + 1 : 0)));
	const fit = $derived(`calc(${unit ? 3.25 : 2.5}rem + ${chars + (unit?.length ?? 0)}ch)`);
	const boxWidth = $derived(`max(${inline ? 'var(--nebu-inline-box, 6.5rem)' : unit ? `calc(5.5rem + ${unit.length}ch)` : '5rem'}, ${fit})`);

	// The box and the knob follow the value
	$effect(() => {
		typed = shown;
		knob = rest;
	});

	// A knob the user moved becomes the value, one the rail moved for itself goes back to rest
	$effect(() => {
		const k = knob;
		untrack(() => {
			if (k === rest) return;
			if (!held) {
				knob = rest;
				return;
			}
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

<div class="field" class:inline class:dense={inline} class:blank style:--box={boxWidth}>
	{#if label}<span class="head"><FieldLabel {label} {info} forId={id} /></span>{/if}
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="range" onpointerdowncapture={() => (held = true)} onkeydowncapture={() => (held = true)} onfocusout={() => (held = false)} onpointerleave={(e) => { if (e.buttons === 0) held = false; }}>
		<NeoRange bind:value={knob} {min} {max} {step} {disabled} elevation={-1} tooltips={false} width="100%" />
	</div>
	<div class="box">
		<NeoInput type="text" inputmode="decimal" {id} bind:value={typed} {disabled} elevation={-1} floating={false} width="100%" after={unit ? after : undefined} onchange={commit} onblur={commit} aria-label={label} />
	</div>
	{#if message}<span class="secondary message">{message}</span>{/if}
</div>

<style>
	/* The label over the rail and the box, or beside the box with the rail under both */
	.field {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		grid-template-areas:
			'label label'
			'range box'
			'message message';
		column-gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
	}
	.field.inline {
		grid-template-columns: minmax(0, 1fr) var(--box);
		grid-template-areas:
			'label box'
			'range range'
			'message message';
	}
	.head {
		grid-area: label;
		min-width: 0;
		margin-bottom: 4px;
	}
	.inline .head {
		margin-bottom: 0;
	}
	/* The kit insets its rail by half a knob, so the rail is pulled back out to the field's edges */
	.range {
		display: flex;
		grid-area: range;
		min-width: 0;
		margin-left: -9px;
		transition: opacity var(--nebu-normal) var(--nebu-ease);
	}
	.inline .range {
		margin-right: -9px;
	}
	.range :global(.neo-range-container) {
		min-width: 0;
	}
	/* An unset rail is empty, its knob showing only under the pointer or the keyboard */
	.blank .range :global(.neo-range-handle) {
		opacity: 0;
	}
	.blank .range:hover :global(.neo-range-handle),
	.blank .range:focus-within :global(.neo-range-handle) {
		opacity: 0.55;
	}
	.blank .range :global(.neo-range-handle-before) {
		background-color: transparent;
	}
	/* The box is as wide as its longest number and its unit need, the rail taking the rest */
	.box {
		display: flex;
		grid-area: box;
		width: var(--box);
		min-width: 0;
	}
	.inline .box {
		width: auto;
	}
	.box :global(.neo-input-group) {
		flex: 1 1 auto;
		min-width: 0;
	}
	.box :global(input) {
		text-align: right;
	}
	.message {
		grid-area: message;
		margin-top: 4px;
	}
	.unit {
		padding: 0 8px 0 4px;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
</style>
