<script lang="ts">
	import type { HTMLInputAttributes } from 'svelte/elements';
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import InfoTip from '#lib/ui/InfoTip.svelte';

	let {
		value = $bindable(''),
		label,
		info,
		placeholder,
		max,
		min,
		mono = false,
		disabled = false,
		readonly = false,
		required = false,
		type = 'text',
		message,
		error,
		autocomplete = 'off',
		onchange
	}: {
		value?: string;
		label?: string;
		info?: string;
		placeholder?: string;
		max?: number;
		min?: number;
		mono?: boolean;
		disabled?: boolean;
		readonly?: boolean;
		required?: boolean;
		type?: 'text' | 'url' | 'email' | 'time';
		message?: string;
		error?: string;
		autocomplete?: HTMLInputAttributes['autocomplete'];
		onchange?: (v: string) => void;
	} = $props();

	const counter = $derived(max ? `${value.length}/${max}` : undefined);
	const hint = $derived(error ?? [message, counter].filter(Boolean).join('  '));
</script>

<div class="field" class:mono>
	<NeoInput bind:value {label} {placeholder} maxlength={max} minlength={min} {disabled} {readonly} {required} {type} elevation={-1} placement={label ? 'top' : 'inside'} floating={false} width="100%" message={hint} {error} {autocomplete} onchange={() => onchange?.(value)} />
	{#if info}<InfoTip text={info} />{/if}
</div>

<style>
	.field {
		display: flex;
		gap: 8px;
		align-items: center;
		min-width: 0;
	}
	.field :global(.neo-input-group) {
		flex: 1 1 auto;
	}
	.mono :global(input) {
		font-family: var(--nebu-font-mono);
		font-size: 13px;
	}
</style>
