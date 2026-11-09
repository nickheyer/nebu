<script lang="ts">
	import type { HTMLInputAttributes } from 'svelte/elements';
	import { NeoPassword } from '#lib/neo/inputs/index.js';
	import { NeoPill } from '#lib/neo/pill/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';

	let {
		value = $bindable(''),
		label,
		info,
		// Whether a secret is stored already
		set = false,
		disabled = false,
		required = false,
		autocomplete = 'off',
		message,
		error
	}: { value?: string; label?: string; info?: string; set?: boolean; disabled?: boolean; required?: boolean; autocomplete?: HTMLInputAttributes['autocomplete']; message?: string; error?: string } = $props();

	const id = fieldId();
</script>

{#snippet stored()}
	{#if set && !value}<NeoPill size="small" text tinted label="set" />{/if}
{/snippet}

<div class="field">
	<FieldLabel {label} {info} forId={id} {required} end={stored} />
	<NeoPassword bind:value {id} placeholder={set && !value ? '••••••••' : ''} {disabled} {required} elevation={-1} buttonProps={{ text: true }} placement="inside" floating={false} width="100%" message={error ?? message} {error} {autocomplete} aria-label={label} />
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
</style>
