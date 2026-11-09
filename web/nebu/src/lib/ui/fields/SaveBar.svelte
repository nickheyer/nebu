<script lang="ts">
	import type { Attachment } from 'svelte/attachments';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { useSheetFoot } from '#lib/ui/layout/sheet.svelte.js';

	// A form's commit row, shown once dirty or always for a create form
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
	const foot = useSheetFoot();

	// Moves the row under a sheet's scrolling form once the sheet offers the place, and takes it away with the form
	const dock: Attachment = (node) => {
		foot?.el?.appendChild(node);
		return () => node.remove();
	};
</script>

{#if always || dirty || error !== undefined}
	<div class="savebar" {@attach dock}>
		{#if status}<span class="msg" class:error-text={error !== undefined} aria-live="polite">{status}</span>{/if}
		<span class="fill"></span>
		<NeoButton text onclick={ondiscard} disabled={saving || (!dirty && !always)}>{always ? 'Cancel' : 'Discard'}</NeoButton>
		<NeoButton class="primary" elevation={2} loading={saving} disabled={disabled || !dirty} onclick={() => onsave()}>{label}</NeoButton>
	</div>
{/if}

<style>
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
