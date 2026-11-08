<script lang="ts">
	import { NeoTextarea } from '#lib/neo/inputs/index.js';

	let {
		value = $bindable(''),
		label,
		placeholder,
		rows = 3,
		maxRows = 8,
		max,
		disabled = false,
		message,
		mono = false
	}: { value?: string; label?: string; placeholder?: string; rows?: number; maxRows?: number; max?: number; disabled?: boolean; message?: string; mono?: boolean } = $props();
</script>

<div class="field" class:mono>
	<NeoTextarea bind:value {label} {placeholder} {rows} maxlength={max} {disabled} elevation={-1} placement={label ? 'top' : 'inside'} floating={false} width="100%" resize="none" {message} autoResize={{ min: rows, max: maxRows }} aria-label={label} />
</div>

<style>
	.field {
		display: flex;
		gap: 8px;
		align-items: flex-start;
		min-width: 0;
	}
	.field :global(.neo-textarea-group),
	.field :global(.neo-input-group) {
		flex: 1 1 auto;
	}
	.mono :global(textarea) {
		font-family: var(--nebu-font-mono);
		font-size: 13px;
	}
</style>
