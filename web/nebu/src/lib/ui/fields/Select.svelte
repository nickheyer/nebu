<script lang="ts" generics="V extends string | number">
	import { NeoSelect } from '#lib/neo/inputs/index.js';
	import type { NeoListItem, NeoListItemContext } from '#lib/neo/list/index.js';
	import InfoTip from '#lib/ui/InfoTip.svelte';
	import type { Option } from '#lib/ui/fields/types.js';

	let {
		value = $bindable(),
		options,
		label,
		info,
		placeholder,
		disabled = false,
		search = false,
		clearable = false,
		required = false,
		message,
		error,
		onchange,
		width
	}: {
		value?: V;
		options: Option<V>[];
		label?: string;
		info?: string;
		placeholder?: string;
		disabled?: boolean;
		search?: boolean;
		clearable?: boolean;
		required?: boolean;
		message?: string;
		error?: string;
		onchange?: (v: V | undefined) => void;
		width?: string;
	} = $props();

	// Each option's tip shows on an info icon at the end of its item
	const tips = $derived(new Map(options.filter((o) => o.tip).map((o) => [String(o.value), o.tip])));
	const items = $derived(options.map((o): NeoListItem<V> => ({ id: String(o.value), value: o.value, label: o.label, description: o.description, disabled: o.disabled, after: o.tip ? optionTip : undefined })));
	const hint = $derived(error ?? (info ? `${message ? `${message}. ` : ''}${info}` : message));
</script>

{#snippet optionTip({ item }: NeoListItemContext<V>)}
	<InfoTip text={tips.get(String(item.value))} />
{/snippet}

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
	placement={label ? 'top' : 'inside'}
	floating={false}
	message={hint}
	{error}
	nullable={clearable}
	width={width ?? '100%'}
	onChange={(v) => onchange?.(v as V | undefined)}
/>
