<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLInputAttributes } from 'svelte/elements';
	import { NeoInput } from '#lib/neo/inputs/index.js';

	let {
		value = $bindable(''),
		label,
		placeholder,
		max,
		min,
		mono = false,
		disabled = false,
		readonly = false,
		required = false,
		type = 'text',
		message,
		error,
		autocomplete = 'off',
		before,
		compact = false,
		onchange
	}: {
		value?: string;
		label?: string;
		placeholder?: string;
		max?: number;
		min?: number;
		mono?: boolean;
		disabled?: boolean;
		readonly?: boolean;
		required?: boolean;
		type?: 'text' | 'url' | 'email' | 'time';
		message?: string;
		error?: string;
		autocomplete?: HTMLInputAttributes['autocomplete'];
		before?: Snippet;
		compact?: boolean;
		onchange?: (v: string) => void;
	} = $props();

	let focused = $state(false);
	const counting = $derived(max !== undefined && (focused || value.length > 0));
</script>

<div class="field" class:mono>
	<div class="box" class:bare={!label}>
		<NeoInput
			bind:value
			{label}
			{placeholder}
			maxlength={max}
			minlength={min}
			{disabled}
			{readonly}
			{required}
			{type}
			elevation={-1}
			placement={label && !compact ? 'top' : 'inside'}
			floating={compact}
			width="100%"
			{message}
			{error}
			{autocomplete}
			{before}
			aria-label={label}
			onfocus={() => (focused = true)}
			onblur={() => (focused = false)}
			onchange={() => onchange?.(value)}
		/>
		{#if counting}<span class="counter" aria-hidden="true">{value.length}/{max}</span>{/if}
	</div>
</div>

<style>
	.field {
		display: flex;
		gap: 8px;
		align-items: flex-end;
		min-width: 0;
	}
	.box {
		position: relative;
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
	}
	.box :global(.neo-input-group) {
		flex: 1 1 auto;
	}
	.counter {
		position: absolute;
		top: 0;
		right: var(--nebu-space-1);
		display: flex;
		align-items: center;
		height: var(--neo-input-label-height, var(--neo-line-height, 1.5rem));
		font-size: var(--nebu-text-caption);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
		pointer-events: none;
	}
	.bare .counter {
		top: 50%;
		right: var(--nebu-space-3);
		height: auto;
		transform: translateY(-50%);
	}
	.mono :global(input) {
		font-family: var(--nebu-font-mono);
		font-size: 13px;
	}
</style>
