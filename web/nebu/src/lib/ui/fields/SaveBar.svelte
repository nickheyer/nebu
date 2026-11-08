<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';

	// A form's commit row pinned under its scroller, shown once dirty or always for a create form
	let {
		dirty,
		always = false,
		saving = false,
		message,
		error,
		label = 'Save',
		disabled = false,
		onsave,
		ondiscard
	}: { dirty: boolean; always?: boolean; saving?: boolean; message?: string; error?: string; label?: string; disabled?: boolean; onsave: () => unknown; ondiscard: () => void } = $props();

	const status = $derived(error ?? message);
</script>

{#if always || dirty || error !== undefined}
	<div class="savebar">
		{#if status}<span class="msg" class:error-text={error !== undefined} aria-live="polite">{status}</span>{/if}
		<span class="fill"></span>
		<NeoButton text onclick={ondiscard} disabled={saving || (!dirty && !always)}>{always ? 'Cancel' : 'Discard'}</NeoButton>
		<NeoButton class="primary" elevation={2} loading={saving} disabled={disabled || !dirty} onclick={() => onsave()}>{label}</NeoButton>
	</div>
{/if}

<style>
	/* Reaches through the scroller's bottom padding so nothing scrolls into view under it */
	.savebar {
		position: sticky;
		bottom: calc(-1 * var(--nebu-sheet-pad, 0px));
		z-index: 2;
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		margin-top: var(--nebu-space-4);
		margin-bottom: calc(-1 * var(--nebu-sheet-pad, 0px));
		padding: var(--nebu-space-3) 0 calc(var(--nebu-space-3) + var(--nebu-sheet-pad, 0px));
		background: var(--neo-background-color);
		border-top: 1px solid var(--nebu-border);
	}
	.msg {
		min-width: 0;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
	}
</style>
