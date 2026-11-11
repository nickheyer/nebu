<script lang="ts">
	import { NeoSwitch } from '#lib/neo/inputs/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';

	// A toggle after its label on one line, on a form's control column or hugging its label when compact
	let {
		checked = $bindable(false),
		label,
		info,
		compact = false,
		disabled = false,
		message,
		onchange
	}: { checked?: boolean; label?: string; info?: string; compact?: boolean; disabled?: boolean; message?: string; onchange?: (on: boolean) => void } = $props();

	const id = fieldId();
</script>

<div class="switchline" class:compact={compact || !label} class:disabled>
	{#if label}<span class="words"><FieldLabel {label} {info} forId={id} /></span>{/if}
	<NeoSwitch {id} bind:checked {disabled} {message} elevation={-1} aria-label={label} onchange={(e) => onchange?.((e.currentTarget as HTMLInputElement).checked)} />
</div>

<style>
	.switchline {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
		min-height: var(--nebu-input-height);
	}
	/* The label column ends where the second column of a two column form starts */
	.words {
		flex: 0 0 min(16rem, calc(50% - var(--nebu-space-3) / 2));
		min-width: 0;
	}
	.compact {
		display: inline-flex;
		gap: var(--nebu-space-2);
		min-height: var(--nebu-control);
	}
	.compact .words {
		flex: 0 1 auto;
	}
	.switchline :global(.neo-switch-container) {
		--neo-label-margin: 0;

		padding-right: 0;
		padding-left: 0;
	}
	.disabled .words {
		color: var(--nebu-disabled-text);
	}
	/* Beside a labelled box the toggle drops to the line of the box */
	:global(.fieldrow:not(.single):has(> :not(.switchline):not(:has(> .switchline))) > .switchline:not(.compact)),
	:global(.fieldrow:not(.single):has(> :not(.switchline):not(:has(> .switchline))) > * > .switchline:not(.compact)) {
		margin-top: 28px;
	}
</style>
