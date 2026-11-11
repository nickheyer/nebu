<script lang="ts" generics="V extends string | number">
	import { NeoSelect } from '#lib/neo/inputs/index.js';
	import type { NeoListItem, NeoListSelectedItem } from '#lib/neo/list/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { fieldId, listPopover, selectToggle, type Option } from '#lib/ui/fields/types.js';

	let {
		value = $bindable([]),
		options,
		label,
		info,
		placeholder,
		disabled = false,
		search = false,
		message,
		// A toolbar select, its label standing as the placeholder
		compact = false
	}: { value?: V[]; options: Option<V>[]; label?: string; info?: string; placeholder?: string; disabled?: boolean; search?: boolean; message?: string; compact?: boolean } = $props();

	const id = fieldId();
	const items = $derived(options.map((o): NeoListItem<V> => ({ id: String(o.value), value: o.value, label: o.label, description: o.description, disabled: o.disabled })));
	const empty = $derived(placeholder ?? (compact ? label : undefined));

	// The chosen options by name on one line
	function named(selection?: NeoListSelectedItem<V> | NeoListSelectedItem<V>[]): string | undefined {
		const picked = Array.isArray(selection) ? selection : selection ? [selection] : [];
		const names = picked.map((s) => s.item?.label ?? s.item?.value?.toString()).filter((n): n is string => !!n);
		return names.length ? names.join(', ') : undefined;
	}
</script>

<div class="field" class:compact>
	{#if !compact}<FieldLabel {label} {info} forId={id} />{/if}
	<NeoSelect
		bind:value
		options={items}
		{id}
		multiple
		placeholder={empty}
		display={named}
		{disabled}
		{search}
		elevation={-1}
		placement="inside"
		floating={false}
		{message}
		clearable
		nullable
		width="100%"
		aria-label={label}
		listProps={listPopover}
		buttonProps={selectToggle}
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
	/* A compact select keeps control height, the chosen options cut off at its edge */
	.compact :global(.neo-input-group) {
		margin-top: 0;
	}
	.compact :global(.neo-input-display-content) {
		overflow: hidden;
		white-space: nowrap;
		text-overflow: ellipsis;
	}
</style>
