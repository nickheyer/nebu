<script lang="ts">
	import type { Snippet } from 'svelte';
	import NeoDialogConfirm from '#lib/neo/floating/dialog/NeoDialogConfirm.svelte';
	import { NeoInput } from '#lib/neo/inputs/index.js';

	let {
		open = $bindable(false),
		title,
		name,
		message,
		confirm = 'Delete',
		// Whether the fields the dialog holds are complete, the typed name aside
		enabled = true,
		onconfirm,
		children
	}: { open?: boolean; title: string; name: string; message?: string; confirm?: string; enabled?: boolean; onconfirm: () => unknown; children?: Snippet } = $props();

	let typed = $state('');
	const ready = $derived(enabled && typed.trim() === name);

	$effect(() => {
		if (!open) typed = '';
	});
</script>

<NeoDialogConfirm bind:open rounded="md" filled elevation={2} width="min(28rem, 92vw)" padding="16px" header={title} disabled={{ confirm: !ready, cancel: false }} confirmProps={{ label: confirm, color: 'error' }} onConfirm={() => onconfirm()}>
	<div class="body">
		{#if message}<p class="secondary">{message}</p>{/if}
		{@render children?.()}
		<NeoInput bind:value={typed} label="Type {name} to confirm" autocomplete="off" elevation={-1} placement="top" floating={false} width="100%" />
	</div>
</NeoDialogConfirm>

<style>
	.body {
		display: flex;
		flex-direction: column;
		gap: 12px;
		min-width: 0;
	}
</style>
