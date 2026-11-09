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
		// A toolbar select, its label standing as the placeholder and prefixed to the chosen options
		compact = false
	}: { value?: V[]; options: Option<V>[]; label?: string; info?: string; placeholder?: string; disabled?: boolean; search?: boolean; message?: string; compact?: boolean } = $props();

	const id = fieldId();
	const items = $derived(options.map((o): NeoListItem<V> => ({ id: String(o.value), value: o.value, label: o.label, description: o.description, disabled: o.disabled })));
	// A compact select names itself in the empty box, every other empty select names the act of choosing
	const empty = $derived(placeholder ?? (compact ? label : 'Select'));

	// The chosen options behind their label, so a toolbar reads Format: GGUF, Safetensors at one control height
	function prefixed(selection?: NeoListSelectedItem<V> | NeoListSelectedItem<V>[]): string | undefined {
		const picked = Array.isArray(selection) ? selection : selection ? [selection] : [];
		const names = picked.map((s) => s.item?.label ?? s.item?.value?.toString()).filter((n): n is string => !!n);
		if (!names.length) return undefined;
		const text = names.length > 2 ? `${names.length} selected` : names.join(', ');
		return compact && label ? `${label}: ${text}` : text;
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
		display={prefixed}
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
	/* A compact select keeps control height, the chosen options shown behind their label */
	.compact :global(.neo-input-group) {
		margin-top: 0;
	}
	.compact :global(.neo-input-display-content) {
		overflow: hidden;
		white-space: nowrap;
		text-overflow: ellipsis;
	}
</style>
