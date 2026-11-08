<script lang="ts">
	import type { HTMLInputAttributes } from 'svelte/elements';
	import { NeoPassword } from '#lib/neo/inputs/index.js';
	import { NeoPill } from '#lib/neo/pill/index.js';
	import InfoTip from '#lib/ui/InfoTip.svelte';

	let {
		value = $bindable(''),
		label,
		info,
		placeholder,
		// Whether a secret is stored already, the field then showing the redaction marker until typed into
		set = false,
		disabled = false,
		required = false,
		autocomplete = 'off',
		message,
		error
	}: { value?: string; label?: string; info?: string; placeholder?: string; set?: boolean; disabled?: boolean; required?: boolean; autocomplete?: HTMLInputAttributes['autocomplete']; message?: string; error?: string } = $props();
</script>

<div class="field">
	<NeoPassword bind:value {label} placeholder={set && !value ? '••••••••' : (placeholder ?? '')} {disabled} {required} elevation={-1} buttonProps={{ text: true }} placement={label ? 'top' : 'inside'} floating={false} width="100%" message={error ?? message} {error} {autocomplete} />
	{#if (set && !value) || info}
		<span class="trail">
			{#if set && !value}<NeoPill size="small" text tinted label="set" />{/if}
			{#if info}<InfoTip text={info} />{/if}
		</span>
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
