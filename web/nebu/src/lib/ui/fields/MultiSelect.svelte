<script lang="ts" generics="V extends string | number">
	import { NeoSelect } from '#lib/neo/inputs/index.js';
	import type { NeoListItem } from '#lib/neo/list/index.js';
	import type { Option } from '#lib/ui/fields/types.js';

	let {
		value = $bindable([]),
		options,
		label,
		info,
		placeholder,
		disabled = false,
		search = false,
		message
	}: { value?: V[]; options: Option<V>[]; label?: string; info?: string; placeholder?: string; disabled?: boolean; search?: boolean; message?: string } = $props();

	const items = $derived(options.map((o): NeoListItem<V> => ({ id: String(o.value), value: o.value, label: o.label, description: o.description, disabled: o.disabled })));
</script>

<NeoSelect bind:value options={items} multiple {label} {placeholder} {disabled} {search} elevation={-1} placement={label ? 'top' : 'inside'} floating={false} message={info ?? message} clearable nullable width="100%" />
