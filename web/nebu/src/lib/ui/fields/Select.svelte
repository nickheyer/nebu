<script lang="ts" generics="V extends string | number">
	import type { Snippet } from 'svelte';
	import { NeoInput, NeoSelect } from '#lib/neo/inputs/index.js';
	import type { NeoListItem } from '#lib/neo/list/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { fieldId, listPopover, selectToggle, type Option } from '#lib/ui/fields/types.js';

	// One choice from a list, plain text in the same box once a single option is all there is
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
		loading = false,
		// A toolbar select, its label standing as the placeholder
		compact = false,
		// Sized to the longest option instead of filling its row
		fit = false,
		icon
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
		loading?: boolean;
		compact?: boolean;
		fit?: boolean;
		icon?: Snippet;
	} = $props();

	const id = fieldId();
	const items = $derived(options.map((o): NeoListItem<V> => ({ id: String(o.value), value: o.value, label: o.label, description: o.description, disabled: o.disabled })));
	const hint = $derived(error ?? message);
	const empty = $derived(placeholder ?? (compact ? label : undefined));
	// The one option there is, once chosen, which leaves nothing to pick
	const only = $derived(options.length === 1 && !clearable && !loading && options[0]!.value === value ? options[0] : undefined);
</script>

<div class="field" class:compact class:fit class:only={only !== undefined}>
	{#if !compact}<FieldLabel {label} {info} forId={id} {required} />{/if}
	<div class="box">
		{#if fit}
			<span class="sizer" class:iconed={icon !== undefined} aria-hidden="true">
				{#each options as o (String(o.value))}<span>{o.label}</span>{/each}
				{#if empty}<span>{empty}</span>{/if}
			</span>
		{/if}
		{#if only}
			<NeoInput value={only.label} {id} readonly {disabled} elevation={-1} placement="inside" floating={false} message={hint} {error} width="100%" before={icon} aria-label={label} />
		{:else}
		<NeoSelect
			bind:value
			options={items}
			{id}
			placeholder={empty}
			{disabled}
			{search}
			{clearable}
			{required}
			elevation={-1}
			placement="inside"
			floating={false}
			{loading}
			message={hint}
			{error}
			nullable={clearable}
			width="100%"
			before={icon}
			aria-label={label}
			listProps={listPopover}
			buttonProps={selectToggle}
			onChange={(v) => onchange?.(v as V | undefined)}
		/>
		{/if}
	</div>
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.box {
		display: grid;
		min-width: 0;
	}
	.box > :global(*) {
		grid-area: 1 / 1;
		min-width: 0;
	}
	.fit {
		flex: 0 0 auto;
	}
	.fit .box {
		width: max-content;
		max-width: 100%;
	}
	/* Every option laid out invisibly so the box is as wide as the longest */
	.sizer {
		display: flex;
		flex-direction: column;
		height: 0;
		padding: 0 4.75rem 0 1.25rem;
		overflow: hidden;
		white-space: nowrap;
		visibility: hidden;
	}
	.sizer.iconed {
		padding-left: 3.75rem;
	}
	/* The one option there is reads as plain text in the box a dropdown would take */
	.only :global(.neo-input-group .neo-input) {
		cursor: default;
	}
	.fit .box :global(.neo-input-group) {
		min-width: 0;
	}
	.fit .box :global(.neo-input:has(> .neo-input-display-content)) {
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
	}
	.fit .box :global(.neo-input-display-content) {
		flex: 1 1 0;
		width: 0;
	}
	/* A compact select keeps control height */
	.compact :global(.neo-input-group) {
		margin-top: 0;
	}
	.compact :global(.neo-input-display-content) {
		overflow: hidden;
		white-space: nowrap;
		text-overflow: ellipsis;
	}
</style>
