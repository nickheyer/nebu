<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoPopConfirm } from '#lib/neo/floating/tooltips/index.js';

	let {
		label,
		question,
		danger = false,
		disabled = false,
		small = true,
		onconfirm,
		icon
	}: { label: string; question: string; danger?: boolean; disabled?: boolean; small?: boolean; onconfirm: () => unknown; icon?: Snippet } = $props();

	let open = $state(false);

	async function run() {
		await onconfirm();
	}
</script>

<NeoPopConfirm bind:open elevation={2} tooltipProps={{ placement: 'bottom-end' }} onConfirm={run} confirmProps={{ label, color: danger ? 'error' : 'success' }}>
	{#snippet tooltip()}<span class="question">{question}</span>{/snippet}
	<NeoButton text={small} elevation={small ? undefined : 2} {disabled} color={danger ? 'error' : undefined} {icon} onclick={() => (open = true)}>{label}</NeoButton>
</NeoPopConfirm>

<style>
	.question {
		display: block;
		max-width: 32ch;
		padding: 4px 4px 0;
		font-size: 13px;
	}
</style>
