<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';
	import { viewport } from '#lib/state/viewport.svelte.js';

	// A modal form. One title, the fields, and a footer with Cancel and the one primary action.
	// `confirm` names the action; leaving it out gives a dialog with Close alone.
	let {
		open = $bindable(false),
		title,
		confirm,
		busy = false,
		disabled = false,
		danger = false,
		width = 'narrow',
		cancel = 'Cancel',
		onconfirm,
		onclose,
		children,
		footer
	}: {
		open?: boolean;
		title: string;
		confirm?: string;
		busy?: boolean;
		disabled?: boolean;
		danger?: boolean;
		width?: 'narrow' | 'wide' | 'full';
		cancel?: string;
		onconfirm?: () => unknown;
		onclose?: () => void;
		children: Snippet;
		// Content in the footer before the buttons, such as a status line
		footer?: Snippet;
	} = $props();

	const widths = { narrow: 'min(28rem, 92vw)', wide: 'min(36rem, 92vw)', full: 'min(48rem, 92vw)' };

	function close() {
		open = false;
		onclose?.();
	}
</script>

<NeoDialog bind:open modal rounded="md" filled elevation={2} width={widths[width]} padding="0" slide={!viewport.reducedMotion} onclose={() => onclose?.()}>
	<div class="dialog">
		<h2 class="title">{title}</h2>
		<div class="body">{@render children()}</div>
		<div class="footer">
			{#if footer}<div class="status">{@render footer()}</div>{/if}
			<span class="fill"></span>
			<NeoButton text onclick={close}>{confirm ? cancel : 'Close'}</NeoButton>
			{#if confirm}
				<NeoButton class={danger ? 'danger' : 'primary'} elevation={2} loading={busy} {disabled} onclick={() => onconfirm?.()}>{confirm}</NeoButton>
			{/if}
		</div>
	</div>
</NeoDialog>

<style>
	/* Fills the dialog's width, which lays its child out as a shrinking flex item */
	.dialog {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		gap: var(--nebu-space-4);
		min-width: 0;
		padding: var(--nebu-space-5);
	}
	.title {
		font-size: var(--nebu-text-heading);
	}
	.body {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-3);
		min-width: 0;
	}
	.footer {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
		padding-top: var(--nebu-space-2);
	}
	.status {
		display: flex;
		min-width: 0;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
	}
</style>
