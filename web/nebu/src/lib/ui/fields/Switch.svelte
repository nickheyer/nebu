<script lang="ts">
	import { NeoSwitch } from '#lib/neo/inputs/index.js';
	import Tip from '#lib/ui/Tip.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';

	let {
		checked = $bindable(false),
		label,
		info,
		top = false,
		disabled = false,
		message,
		onchange
	}: { checked?: boolean; label?: string; info?: string; top?: boolean; disabled?: boolean; message?: string; onchange?: (on: boolean) => void } = $props();

	const id = fieldId();
</script>

{#if top}
	<div class="field">
		<FieldLabel {label} {info} forId={id} />
		<div class="switch control">
			<NeoSwitch {id} bind:checked {disabled} {message} elevation={-1} aria-label={label} onchange={(e) => onchange?.((e.currentTarget as HTMLInputElement).checked)} />
		</div>
	</div>
{:else}
	<div class="switch">
		<NeoSwitch {id} bind:checked {disabled} {message} elevation={-1} aria-label={label} onchange={(e) => onchange?.((e.currentTarget as HTMLInputElement).checked)} />
		{#if label}
			<Tip text={info} tag="span"><label class="text" class:disabled class:informed={info !== undefined} for={id}>{label}</label></Tip>
		{/if}
	</div>
{/if}

<style>
	.switch {
		display: inline-flex;
		gap: 6px;
		align-items: center;
		min-width: 0;
		min-height: var(--nebu-control);
	}
	.text {
		margin-left: 4px;
		cursor: pointer;
	}
	.text.informed {
		text-decoration: underline dotted var(--nebu-text-faint);
		text-decoration-thickness: 1px;
		text-underline-offset: 3px;
	}
	.text.disabled {
		color: var(--nebu-disabled-text);
		cursor: default;
	}
	/* Stretches to the row so the toggle centres on a neighbouring input box under its own label */
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		align-self: stretch;
		min-width: 0;
	}
	.control {
		flex: 1 1 auto;
		min-height: var(--nebu-input-height);
	}
</style>
