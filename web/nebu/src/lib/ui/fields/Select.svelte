<script lang="ts" generics="V extends string | number">
	import type { Snippet } from 'svelte';
	import { NeoSelect } from '#lib/neo/inputs/index.js';
	import type { NeoListItem } from '#lib/neo/list/index.js';
	import type { Option } from '#lib/ui/fields/types.js';

	let {
		value = $bindable(),
		options,
		label,
		placeholder,
		disabled = false,
		search = false,
		clearable = false,
		required = false,
		message,
		error,
		onchange,
		width,
		loading = false,
		compact = false,
		icon,
		mark
	}: {
		value?: V;
		options: Option<V>[];
		label?: string;
		placeholder?: string;
		disabled?: boolean;
		search?: boolean;
		clearable?: boolean;
		required?: boolean;
		message?: string;
		error?: string;
		onchange?: (v: V | undefined) => void;
		width?: string;
		loading?: boolean;
		compact?: boolean;
		icon?: Snippet;
		// A mark after the box for how an empty box resolves
		mark?: Snippet;
	} = $props();

	const items = $derived(options.map((o): NeoListItem<V> => ({ id: String(o.value), value: o.value, label: o.label, description: o.description, disabled: o.disabled })));
	const hint = $derived(error ?? message);
</script>

<div class="field" class:compact>
	<NeoSelect
		bind:value
		options={items}
		{label}
		{placeholder}
		{disabled}
		{search}
		{clearable}
		{required}
		elevation={-1}
		placement={label && !compact ? 'top' : 'inside'}
		floating={compact}
		{loading}
		message={hint}
		{error}
		nullable={clearable}
		width={width ?? '100%'}
		before={icon}
		aria-label={label}
		onChange={(v) => onchange?.(v as V | undefined)}
	/>
	{#if mark}<span class="mark">{@render mark()}</span>{/if}
</div>

<style>
	.field {
		display: flex;
		gap: 8px;
		align-items: flex-end;
		min-width: 0;
	}
	.field :global(.neo-input-group),
	.field :global(.neo-select) {
		flex: 1 1 auto;
		min-width: 0;
	}
	/* A compact select keeps control height with its label floating inside */
	.compact :global(.neo-input-group) {
		margin-top: 0;
	}
	.mark {
		display: inline-flex;
		color: var(--nebu-text-muted);
	}
</style>
