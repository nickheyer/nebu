<script lang="ts" generics="V extends string | number">
	import { NeoSelect } from '#lib/neo/inputs/index.js';
	import type { NeoListItem } from '#lib/neo/list/index.js';
	import type { Option } from '#lib/ui/fields/types.js';

	let {
		value = $bindable([]),
		options,
		label,
		disabled = false,
		search = false,
		message,
		compact = false
	}: { value?: V[]; options: Option<V>[]; label?: string; disabled?: boolean; search?: boolean; message?: string; compact?: boolean } = $props();

	const items = $derived(options.map((o): NeoListItem<V> => ({ id: String(o.value), value: o.value, label: o.label, description: o.description, disabled: o.disabled })));
</script>

<NeoSelect bind:value options={items} multiple {label} {disabled} {search} elevation={-1} placement={label && !compact ? 'top' : 'inside'} floating={compact} {message} clearable nullable width="100%" aria-label={label} />
