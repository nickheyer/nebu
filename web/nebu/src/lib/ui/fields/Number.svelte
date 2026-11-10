<script lang="ts">
	import { untrack } from 'svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import Stepper from '#lib/ui/fields/Stepper.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';

	// A number box, empty when unset or zero under emptyZero, the empty box showing its resolved number
	let {
		value = $bindable(),
		min,
		max,
		step = 1,
		label,
		info,
		fallback,
		unit,
		disabled = false,
		message,
		error,
		emptyZero = false,
		optional = false,
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
		unit?: string;
		disabled?: boolean;
		message?: string;
		error?: string;
		emptyZero?: boolean;
		optional?: boolean;
		onchange?: (n: number) => void;
		onclear?: () => void;
	} = $props();

	const id = fieldId();
	let focused = $state(false);
	const shown = $derived(value === undefined || (emptyZero && value === 0) ? '' : String(value));
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
		if (!globalThis.Number.isFinite(n) || (min !== undefined && n < min) || (max !== undefined && n > max)) return;
		value = n;
		onchange?.(n);
	}
</script>

<div class="field">
	<FieldLabel {label} {info} forId={id} />
	<Stepper {id} bind:raw bind:focused {min} {max} {step} {fallback} {disabled} {message} {error} {unit} ariaLabel={label} onchange={changed} />
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
</style>
