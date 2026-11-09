<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoPopConfirm } from '#lib/neo/floating/tooltips/index.js';

	// A button that asks its question in a popover before it acts
	let {
		label,
		question,
		danger = false,
		disabled = false,
		small = true,
		onconfirm,
		icon,
		body
	}: { label: string; question: string; danger?: boolean; disabled?: boolean; small?: boolean; onconfirm: () => unknown; icon?: Snippet; body?: Snippet } = $props();

	let open = $state(false);

	async function run() {
		await onconfirm();
	}
</script>

<NeoPopConfirm
	bind:open
	elevation={2}
	closable={false}
	tooltipProps={{ placement: 'bottom-end', portal: true, openOnHover: false, openOnFocus: false }}
	onConfirm={run}
	cancelProps={{ label: 'Cancel', color: undefined, text: true, class: 'confirm-cancel' }}
	confirmProps={{ label, color: danger ? 'error' : 'primary', text: true }}
>
	{#snippet tooltip()}
		<div class="ask">
			<span class="question">{question}</span>
			{#if body}<div class="body">{@render body()}</div>{/if}
		</div>
	{/snippet}
	<NeoButton flat={small} elevation={small ? undefined : 2} {disabled} color={danger ? 'error' : undefined} {icon} onclick={() => (open = true)}>{label}</NeoButton>
</NeoPopConfirm>

<style>
	.ask {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-3);
		min-width: 0;
		max-width: 32ch;
		padding: var(--nebu-space-1) var(--nebu-space-1) 0;
	}
	.question {
		font-size: var(--nebu-text-secondary);
	}
	.body {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	:global(.neo-button.confirm-cancel) {
		--neo-btn-text-color: var(--nebu-text-muted);
		--neo-btn-text-color-hover: var(--neo-text-color);
		--neo-btn-text-color-active: var(--neo-text-color);
	}
</style>
