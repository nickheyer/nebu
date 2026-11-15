<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { Attachment } from 'svelte/attachments';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { useSheetFoot } from '#lib/ui/layout/sheet.svelte.js';

	// A form's commit row, always in view, saying what the form still needs or that nothing in it has changed
	let {
		dirty,
		// A form that makes something new, which commits without a change and cancels instead of discarding
		always = false,
		saving = false,
		message,
		error,
		label = 'Save',
		// What the form still needs before it can commit, empty once it needs nothing
		needs = '',
		onsave,
		ondiscard,
		leading
	}: { dirty: boolean; always?: boolean; saving?: boolean; message?: string; error?: string; label?: string; needs?: string; onsave: () => unknown; ondiscard: () => void; leading?: Snippet } = $props();

	// The error first, then what is missing, then the form's own word, then that there is nothing to save
	const status = $derived(error ?? (needs || message || (dirty || always ? undefined : 'No changes')));
	const foot = useSheetFoot();

	// Moves the row under a sheet's scrolling form once the sheet offers the place, and takes it away with the form
	const dock: Attachment = (node) => {
		foot?.el?.appendChild(node);
		return () => node.remove();
	};
</script>

<div class="savebar" {@attach dock}>
	{#if leading}{@render leading()}{/if}
	<span class="fill"></span>
	{#if status}<span class="msg" class:error-text={error !== undefined} aria-live="polite">{status}</span>{/if}
	<NeoButton text onclick={ondiscard} disabled={saving || (!dirty && !always)}>{always ? 'Cancel' : 'Discard'}</NeoButton>
	<NeoButton class="primary" elevation={2} loading={saving} disabled={needs !== '' || !dirty} onclick={() => onsave()}>{label}</NeoButton>
</div>

<style>
	.savebar {
		position: sticky;
		bottom: calc(var(--nebu-bottom, 0px) - var(--nebu-sheet-pad, 0px));
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
	/* What the row has to say stands beside the buttons it explains */
	.msg {
		min-width: 0;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		text-align: right;
		overflow-wrap: anywhere;
	}
</style>
