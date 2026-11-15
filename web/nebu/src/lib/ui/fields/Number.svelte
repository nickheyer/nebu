<script lang="ts">
	import { untrack } from 'svelte';
	import { num } from '#lib/format.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import Resting from '#lib/ui/fields/Resting.svelte';
	import Stepper from '#lib/ui/fields/Stepper.svelte';
	import type { Named } from '#lib/ui/fields/scale.js';
	import { fieldId } from '#lib/ui/fields/types.js';

	// A number box, empty when unset or zero under emptyZero with what it comes to written under it, a named number read by its name until the box is entered
	let {
		value = $bindable(),
		min,
		max,
		step = 1,
		label,
		info,
		fallback,
		// What an empty box comes to in words, when no number says it
		unset,
		// Leaves the line under an empty box to the form that draws its own
		caption = true,
		placeholder,
		unit,
		disabled = false,
		message,
		error,
		emptyZero = false,
		optional = false,
		// The label beside a short box at the row's end, for a narrow column of settings
		inline = false,
		// A box alone, without the buttons that step it
		plain = false,
		// Takes a decimal point whatever the step
		fraction = false,
		// Numbers shown by what they mean, such as -1 for random
		names = [],
		// The name a box without a label goes by
		aria,
		onchange,
		onclear
	}: {
		value?: number;
		min?: number;
		max?: number;
		step?: number;
		label?: string;
		info?: string;
		fallback?: number;
		unset?: string;
		caption?: boolean;
		placeholder?: string;
		unit?: string;
		disabled?: boolean;
		message?: string;
		error?: string;
		emptyZero?: boolean;
		optional?: boolean;
		inline?: boolean;
		plain?: boolean;
		fraction?: boolean;
		names?: Named[];
		aria?: string;
		onchange?: (n: number) => void;
		onclear?: () => void;
	} = $props();

	const id = fieldId();
	let focused = $state(false);
	const shown = $derived(value === undefined || (emptyZero && value === 0) ? '' : String(value));
	const rest = $derived(unset ?? (fallback === undefined ? undefined : [globalThis.Number.isInteger(fallback) ? num(fallback) : String(fallback), unit].filter(Boolean).join(' ')));
	const alias = $derived(value === undefined || focused ? undefined : names.find((n) => n.value === value)?.label);
	let raw = $state<number | string | null>(untrack(() => shown));

	// The box follows the value except while being typed in
	$effect(() => {
		const next = shown;
		if (!focused) raw = next;
	});

	// Takes a number within bounds, an emptied box unsetting an optional value, bad text left to the box's error
	function changed() {
		if (raw === '' || raw === null || raw === undefined) {
			if (optional) {
				value = undefined;
				onclear?.();
			} else if (emptyZero) {
				value = 0;
				onchange?.(0);
			}
			return;
		}
		const n = globalThis.Number(raw);
		if (!globalThis.Number.isFinite(n)) return;
		if (!names.some((x) => x.value === n) && ((min !== undefined && n < min) || (max !== undefined && n > max))) return;
		value = n;
		onchange?.(n);
	}
</script>

<div class="field" class:inline class:dense={inline}>
	<FieldLabel {label} {info} forId={id} />
	<div class="box" class:aliased={alias !== undefined}>
		<Stepper {id} bind:raw bind:focused {min} {max} {step} {fallback} {placeholder} {disabled} {message} {error} unit={alias === undefined ? unit : undefined} buttons={!inline && !plain} {fraction} allowed={names.map((n) => n.value)} ariaLabel={label ?? aria} onchange={changed} />
		{#if alias !== undefined}<span class="alias" aria-hidden="true">{alias}</span>{/if}
	</div>
	{#if caption && rest !== undefined && shown === ''}<div class="rest"><Resting text={rest} /></div>{/if}
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.box {
		position: relative;
		display: flex;
		min-width: 0;
	}
	/* A named number reads by its name over the box, the digits back as soon as the box is entered */
	.aliased :global(input) {
		color: transparent;
	}
	.alias {
		position: absolute;
		top: 0;
		left: 0.75rem;
		display: flex;
		align-items: center;
		height: var(--nebu-input-height);
		pointer-events: none;
	}
	.inline {
		flex-flow: row wrap;
		gap: 0 var(--nebu-space-3);
		align-items: center;
		justify-content: space-between;
	}
	.inline .box {
		flex: 0 0 var(--nebu-inline-box, 6.5rem);
	}
	.inline :global(input) {
		text-align: right;
	}
	.inline .rest {
		flex: 1 1 100%;
	}
</style>
