<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoCollapse } from '#lib/neo/collapse/index.js';
	import CaretDownIcon from 'phosphor-svelte/lib/CaretDownIcon';

	// A short section under one trigger, its body set in from the caret unless told to line up with it
	let { label, open = $bindable(false), heading = false, indent = true, children }: { label: string; open?: boolean; heading?: boolean; indent?: boolean; children: Snippet } = $props();
</script>

{#snippet trigger()}
	<span class="trigger" class:heading>
		<span class="caret" class:open><CaretDownIcon size={16} /></span>
		<span class="text">{label}</span>
	</span>
{/snippet}

<NeoCollapse bind:open label={trigger}>
	<div class="body" class:indent>{@render children()}</div>
</NeoCollapse>

<style>
	.trigger {
		display: inline-flex;
		gap: 6px;
		align-items: center;
	}
	/* A section title doubling as the trigger keeps the weight of the headings beside it */
	.trigger.heading .text {
		font-size: var(--nebu-text-body);
		font-weight: 500;
		letter-spacing: -0.01em;
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
		padding: 4px 0 8px;
	}
	.body.indent {
		padding-left: 22px;
	}
</style>
