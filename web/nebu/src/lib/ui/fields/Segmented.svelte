<script lang="ts" generics="V extends string | number | boolean">
	import { NeoButton, NeoButtonGroup } from '#lib/neo/buttons/index.js';
	import Tip from '#lib/ui/Tip.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import type { Option } from '#lib/ui/fields/types.js';

	let {
		value = $bindable(),
		options,
		label,
		info,
		disabled = false,
		onchange
	}: { value?: V; options: Option<V>[]; label?: string; info?: string; disabled?: boolean; onchange?: (v: V) => void } = $props();

	function pick(v: V) {
		if (disabled) return;
		value = v;
		onchange?.(v);
	}
</script>

<div class="field">
	<FieldLabel {label} {info} />
	<NeoButtonGroup elevation={-1} role="radiogroup" aria-label={label}>
		{#each options as o (String(o.value))}
			<Tip text={o.tip} tag="span">
				<NeoButton
					role="radio"
					aria-checked={value === o.value}
					checked={value === o.value}
					toggle
					disabled={disabled || o.disabled}
					color={value === o.value ? 'primary' : undefined}
					onclick={() => pick(o.value)}
				>
					{o.label}
				</NeoButton>
			</Tip>
		{/each}
	</NeoButtonGroup>
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
</style>
