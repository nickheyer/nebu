<script lang="ts">
	import type { HTMLInputAttributes } from 'svelte/elements';
	import { NeoPassword } from '#lib/neo/inputs/index.js';
	import { NeoPill } from '#lib/neo/pill/index.js';

	let {
		value = $bindable(''),
		label,
		// Whether a secret is stored already
		set = false,
		disabled = false,
		required = false,
		autocomplete = 'off',
		message,
		error
	}: { value?: string; label?: string; set?: boolean; disabled?: boolean; required?: boolean; autocomplete?: HTMLInputAttributes['autocomplete']; message?: string; error?: string } = $props();
</script>

<div class="field">
	<NeoPassword bind:value {label} placeholder={set && !value ? '••••••••' : ''} {disabled} {required} elevation={-1} buttonProps={{ text: true }} placement={label ? 'top' : 'inside'} floating={false} width="100%" message={error ?? message} {error} {autocomplete} aria-label={label} />
	{#if set && !value}
		<span class="trail"><NeoPill size="small" text tinted label="set" /></span>
	{/if}
</div>

<style>
	.field {
		display: flex;
		gap: 8px;
		align-items: flex-end;
		min-width: 0;
	}
	.field :global(.neo-input-group) {
		flex: 1 1 auto;
	}
</style>
