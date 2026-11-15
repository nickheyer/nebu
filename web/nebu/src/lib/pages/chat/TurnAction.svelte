<script lang="ts">
	import type { Component } from 'svelte';
	import type { IconComponentProps } from 'phosphor-svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoPopConfirm } from '#lib/neo/floating/tooltips/index.js';

	// One small action under a message, asking its question in a popover first when it has one
	let {
		label,
		icon: Icon,
		question,
		confirm = label,
		danger = false,
		onact
	}: { label: string; icon: Component<IconComponentProps>; question?: string; confirm?: string; danger?: boolean; onact: () => unknown } = $props();

	let open = $state(false);
</script>

{#snippet button(onclick: () => unknown)}
	<NeoButton text aria-label={label} title={label} class="turn-action" {onclick}>
		{#snippet icon()}<Icon size={16} />{/snippet}
	</NeoButton>
{/snippet}

{#if question}
	<NeoPopConfirm
		bind:open
		elevation={2}
		closable={false}
		tooltipProps={{ placement: 'top', portal: true, openOnHover: false, openOnFocus: false }}
		onConfirm={() => onact()}
		cancelProps={{ label: 'Cancel', color: undefined, text: true }}
		confirmProps={{ label: confirm, color: danger ? 'error' : 'primary', text: true }}
	>
		{#snippet tooltip()}<span class="question">{question}</span>{/snippet}
		{@render button(() => (open = true))}
	</NeoPopConfirm>
{:else}
	{@render button(onact)}
{/if}

<style>
	:global(.turn-action) {
		--neo-btn-padding-empty: 6px;
		--neo-btn-margin: 0;
	}
	.question {
		display: block;
		max-width: 32ch;
		font-size: var(--nebu-text-secondary);
	}
</style>
