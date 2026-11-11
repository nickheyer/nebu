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
		after,
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
		after?: Snippet;
		compact?: boolean;
		onchange?: (v: string) => void;
	} = $props();

	const id = fieldId();
</script>

<div class="field" class:monofield={mono} class:compact>
	{#if !compact}<FieldLabel {label} {info} forId={id} {required} />{/if}
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
		{after}
		aria-label={label}
		onchange={() => onchange?.(value)}
	/>
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.field :global(.neo-input-group) {
		min-width: 0;
	}
	.compact :global(.neo-input-group) {
		margin-top: 0;
	}
	.monofield :global(input) {
		font-family: var(--nebu-font-mono);
		font-size: 13px;
	}
</style>
