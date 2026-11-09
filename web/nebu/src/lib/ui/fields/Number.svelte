<script lang="ts">
	import { untrack } from 'svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import Stepper from '#lib/ui/fields/Stepper.svelte';
	import { fieldId, type Off } from '#lib/ui/fields/types.js';

	let {
		value = $bindable(0),
		min,
		max,
		step = 1,
		label,
		info,
		placeholder,
		unit,
		disabled = false,
		message,
		error,
		// Whether zero shows as an empty box
		emptyZero = false,
		// The toggle emptying the box, zero then meaning what its label says
		off,
		onchange
	}: { value?: number; min?: number; max?: number; step?: number; label?: string; info?: string; placeholder?: string; unit?: string; disabled?: boolean; message?: string; error?: string; emptyZero?: boolean; off?: Off; onchange?: (n: number) => void } = $props();

	const id = fieldId();
	let focused = $state(false);
	let raw = $state<number | string | null>(untrack(() => (emptyZero && value === 0 ? '' : String(value))));
	const isOff = $derived(emptyZero && value === 0);

	// The box follows the value except while being typed in
	$effect(() => {
		const next = emptyZero && value === 0 ? '' : String(value);
		if (!focused) raw = next;
	});

	// Takes a number the bounds allow, leaving text that is none or past them to the box's own error
	function changed() {
		const blank = raw === '' || raw === null || raw === undefined;
		const n = blank ? 0 : globalThis.Number(raw);
		if ((blank && !emptyZero) || !globalThis.Number.isFinite(n)) return;
		if (!blank && ((min !== undefined && n < min) || (max !== undefined && n > max))) return;
		value = n;
		onchange?.(n);
	}
</script>

<div class="field">
	<FieldLabel {label} {info} forId={id} />
	<Stepper {id} bind:raw bind:focused {min} {max} {step} placeholder={placeholder ?? (isOff && off ? off.label : '')} {disabled} {message} {error} {unit} off={emptyZero ? off : undefined} {isOff} ariaLabel={label} onchange={changed} />
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
</style>
