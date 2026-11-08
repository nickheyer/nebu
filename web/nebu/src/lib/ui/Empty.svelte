<script lang="ts">
	import type { Component } from 'svelte';
	import type { IconComponentProps } from 'phosphor-svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import RowAction from '#lib/ui/RowAction.svelte';

	// An empty block with a line, an optional hint and at most one button
	let {
		text,
		hint,
		icon: Icon,
		action,
		href,
		onclick,
		loading = false,
		primary = false,
		row = false
	}: {
		text: string;
		hint?: string;
		icon?: Component<IconComponentProps>;
		action?: string;
		href?: string;
		onclick?: () => unknown;
		loading?: boolean;
		primary?: boolean;
		row?: boolean;
	} = $props();

	const hasAction = $derived(action !== undefined && (href !== undefined || onclick !== undefined));
</script>

{#if row}
	<div class="emptyrow">
		<span class="text muted">{text}</span>
		{#if hasAction}
			<RowAction label={action ?? ''} {href} {onclick} {loading} />
		{/if}
	</div>
{:else}
	<div class="empty">
		{#if Icon}<span class="icon" aria-hidden="true"><Icon size={28} /></span>{/if}
		<p class="text unmeasured">{text}</p>
		{#if hint}<p class="hint">{hint}</p>{/if}
		{#if hasAction}
			<span class="button"><NeoButton class={primary ? 'primary' : undefined} elevation={2} {href} {onclick} {loading}>{action}</NeoButton></span>
		{/if}
	</div>
{/if}

<style>
	.emptyrow {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		justify-content: space-between;
		min-height: 44px;
		padding: var(--nebu-space-1) var(--nebu-row-inset, var(--nebu-space-2));
	}
	.text {
		min-width: 0;
	}
	.empty {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		align-items: center;
		width: 100%;
		padding: var(--nebu-space-7) var(--nebu-space-5);
		text-align: center;
	}
	.icon {
		display: inline-flex;
		margin-bottom: var(--nebu-space-1);
		color: var(--nebu-text-faint);
	}
	.empty .text {
		color: var(--neo-text-color);
	}
	.hint {
		max-width: 52ch;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
	}
	.button {
		display: inline-flex;
		margin-top: var(--nebu-space-3);
	}
</style>
