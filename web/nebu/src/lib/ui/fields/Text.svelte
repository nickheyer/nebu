<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLInputAttributes } from 'svelte/elements';
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import Resting from '#lib/ui/fields/Resting.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';

	let {
		value = $bindable(''),
		label,
		info,
		placeholder,
		// What an empty box comes to, written under it
		unset,
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
		// A toolbar field, its label inside the box before the text unless an icon stands there
		compact = false,
		aria,
		onchange
	}: {
		value?: string;
		label?: string;
		info?: string;
		placeholder?: string;
		unset?: string;
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
		aria?: string;
		onchange?: (v: string) => void;
	} = $props();

	const id = fieldId();
</script>

{#snippet lead()}
	<span class="inlabel">{label}</span>
{/snippet}

<div class="field" class:monofield={mono} class:compact>
	{#if !compact}<FieldLabel {label} {info} forId={id} {required} />{/if}
	<NeoInput
		bind:value
		{id}
		placeholder={placeholder ?? (compact && before ? label : undefined)}
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
		before={compact && label && !before ? lead : before}
		{after}
		aria-label={label ?? aria}
		onchange={() => onchange?.(value)}
	/>
	{#if unset && value === ''}<Resting text={unset} />{/if}
</div>

<style>
	/* A toolbar field keeps its name in the box before what it holds */
	.inlabel {
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
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
