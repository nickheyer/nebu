<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLInputAttributes } from 'svelte/elements';
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';

	let {
		value = $bindable(''),
		label,
		info,
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
		// A toolbar field, its label standing as the placeholder
		compact = false,
		onchange
	}: {
		value?: string;
		label?: string;
		info?: string;
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

	const id = fieldId();
	let focused = $state(false);
	const counting = $derived(max !== undefined && (focused || value.length > 0));
</script>

{#snippet counter()}
	{#if counting}<span class="counter" aria-hidden="true">{value.length}/{max}</span>{/if}
{/snippet}

<div class="field" class:monofield={mono} class:compact>
	{#if !compact}<FieldLabel {label} {info} forId={id} {required} end={counter} />{/if}
	<div class="box">
		<NeoInput
			bind:value
			{id}
			placeholder={placeholder ?? (compact ? label : undefined)}
			maxlength={max}
			minlength={min}
			{disabled}
			{readonly}
			{required}
			{type}
			elevation={-1}
			placement="inside"
			floating={false}
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
		{#if compact}{@render counter()}{/if}
	</div>
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
	.box :global(.neo-input-group) {
		flex: 1 1 auto;
		min-width: 0;
	}
	.counter {
		font-size: var(--nebu-text-caption);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
	.box > .counter {
		position: absolute;
		top: 50%;
		right: var(--nebu-space-3);
		transform: translateY(-50%);
		pointer-events: none;
	}
	.compact :global(.neo-input-group) {
		margin-top: 0;
	}
	.monofield :global(input) {
		font-family: var(--nebu-font-mono);
		font-size: 13px;
	}
</style>
