<script lang="ts">
	import { NeoTextarea } from '#lib/neo/inputs/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';

	let {
		value = $bindable(''),
		label,
		info,
		placeholder,
		rows = 3,
		maxRows = 8,
		max,
		disabled = false,
		message,
		mono = false
	}: { value?: string; label?: string; info?: string; placeholder?: string; rows?: number; maxRows?: number; max?: number; disabled?: boolean; message?: string; mono?: boolean } = $props();

	const id = fieldId();
</script>

<div class="field" class:monofield={mono}>
	<FieldLabel {label} {info} forId={id} />
	<NeoTextarea bind:value {id} {placeholder} {rows} maxlength={max} {disabled} elevation={-1} placement="inside" floating={false} width="100%" resize="none" {message} autoResize={{ min: rows, max: maxRows }} aria-label={label} />
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.field :global(.neo-textarea-group) {
		min-width: 0;
	}
	.monofield :global(textarea) {
		font-family: var(--nebu-font-mono);
		font-size: 13px;
	}
</style>
