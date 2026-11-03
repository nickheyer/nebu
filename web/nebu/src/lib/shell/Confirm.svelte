<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';

	let {
		open = $bindable(false),
		title,
		message,
		confirm = 'Confirm',
		danger = false,
		busy = false,
		onconfirm,
		children
	}: {
		open?: boolean;
		title: string;
		message?: string;
		confirm?: string;
		danger?: boolean;
		busy?: boolean;
		onconfirm: () => unknown;
		children?: Snippet;
	} = $props();
</script>

<NeoDialog bind:open modal rounded elevation={3} width="min(32rem, 90vw)" padding="1.5rem">
	<h2 class="title">{title}</h2>
	{#if message}<p class="message">{message}</p>{/if}
	{@render children?.()}
	<div class="actions">
		<NeoButton text onclick={() => (open = false)} disabled={busy}>Cancel</NeoButton>
		<NeoButton filled color={danger ? 'error' : 'primary'} loading={busy} onclick={() => onconfirm()}>{confirm}</NeoButton>
	</div>
</NeoDialog>

<style>
	.title {
		margin: 0 0 var(--neo-gap-sm);
		font-size: var(--neo-font-size-lg);
	}
	.message {
		margin: 0 0 var(--neo-gap);
		color: var(--neo-text-color-secondary);
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--neo-gap-sm);
		margin-top: var(--neo-gap);
	}
</style>
