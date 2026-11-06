<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoCollapse } from '#lib/neo/collapse/index.js';
	import CaretDownIcon from 'phosphor-svelte/lib/CaretDownIcon';

	// A short section under one trigger, the caret turning as it opens, as the row expand does
	let { label, count, open = $bindable(false), children }: { label: string; count?: number | string; open?: boolean; children: Snippet } = $props();
</script>

{#snippet trigger()}
	<span class="trigger">
		<span class="caret" class:open><CaretDownIcon size={16} /></span>
		<span class="text">{label}</span>
		{#if count !== undefined}<span class="count">{count}</span>{/if}
	</span>
{/snippet}

<NeoCollapse bind:open label={trigger}>
	<div class="body">{@render children()}</div>
</NeoCollapse>

<style>
	.trigger {
		display: inline-flex;
		gap: 6px;
		align-items: center;
		font-weight: 500;
	}
	.caret {
		display: inline-flex;
		color: var(--nebu-text-muted);
		transition: transform 150ms ease-out;
	}
	.caret.open {
		transform: rotate(180deg);
	}
	.count {
		font-size: 13px;
		color: var(--nebu-text-muted);
	}
	.body {
		padding: 4px 0 8px 22px;
	}
	@media (prefers-reduced-motion: reduce) {
		.caret {
			transition: none;
		}
	}
</style>
