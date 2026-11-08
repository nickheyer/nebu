<script lang="ts">
	import { untrack } from 'svelte';
	import { NeoNumberStep } from '#lib/neo/inputs/index.js';
	import InfoTip from '#lib/ui/InfoTip.svelte';

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
		// Whether zero shows as a blank box with the placeholder, clearing the box setting zero
		emptyZero = false,
		onchange
	}: { value?: number; min?: number; max?: number; step?: number; label?: string; info?: string; placeholder?: string; unit?: string; disabled?: boolean; message?: string; error?: string; emptyZero?: boolean; onchange?: (n: number) => void } = $props();

	let raw = $state<number | string | null | undefined>(untrack(() => (emptyZero && value === 0 ? '' : value)));

	$effect(() => {
		raw = emptyZero && value === 0 ? '' : value;
	});

	function changed() {
		const blank = raw === '' || raw === null || raw === undefined;
		const n = blank ? 0 : globalThis.Number(raw);
		if ((blank && !emptyZero) || globalThis.Number.isNaN(n)) return;
		value = n;
		onchange?.(n);
	}
</script>

<div class="field">
	<NeoNumberStep bind:value={raw} {min} {max} {step} {label} {placeholder} {disabled} elevation={-1} placement={label ? 'top' : 'inside'} floating={false} width="100%" message={error ?? message} {error} oninput={changed} onchange={changed} center={false} />
	{#if unit || info}
		<span class="trail">
			{#if unit}<span class="unit secondary">{unit}</span>{/if}
			{#if info}<InfoTip text={info} />{/if}
		</span>
	{/if}
</div>

<style>
	.field {
		display: flex;
		gap: 8px;
		align-items: flex-end;
		min-width: 0;
	}
	.field :global(.neo-input-group) {
		flex: 1 1 auto;
	}
	.unit {
		white-space: nowrap;
	}
</style>
