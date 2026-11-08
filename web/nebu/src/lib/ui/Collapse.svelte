<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoCollapse } from '#lib/neo/collapse/index.js';
	import CaretDownIcon from 'phosphor-svelte/lib/CaretDownIcon';

	// A short section under one trigger
	let { label, open = $bindable(false), children }: { label: string; open?: boolean; children: Snippet } = $props();
</script>

{#snippet trigger()}
	<span class="trigger">
		<span class="caret" class:open><CaretDownIcon size={16} /></span>
		<span class="text">{label}</span>
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
	}
	.caret {
		display: inline-flex;
		color: var(--nebu-text-muted);
		transition: transform var(--nebu-normal) var(--nebu-settle);
	}
	.caret.open {
		transform: rotate(180deg);
	}
	.body {
		padding: 4px 0 8px 22px;
	}
</style>
