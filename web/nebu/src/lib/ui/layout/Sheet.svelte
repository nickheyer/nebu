<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';
	import { NeoDrawer } from '#lib/neo/floating/drawer/index.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import { hatch } from '#lib/ui/motion.svelte.js';

	// The list and the open record, beside it, in a drawer or full screen by width
	let {
		open = $bindable(false),
		onclose,
		head,
		list,
		children,
		width = 560
	}: {
		open?: boolean;
		onclose?: () => void;
		head?: Snippet;
		list?: Snippet;
		children: Snippet;
		width?: number;
	} = $props();

	let pageWidth = $state(0);
	const beside = $derived(list !== undefined && !viewport.mobile && pageWidth >= 1080);

	function closed() {
		open = false;
		onclose?.();
	}
</script>

<div class="sheetwrap" bind:clientWidth={pageWidth}>
	{#if head}<div class="head">{@render head()}</div>{/if}
	{#if beside && open}
		<div class="split">
			<div class="list">{@render list?.()}</div>
			<aside class="pane">
				<div class="panel" in:hatch>{@render children()}</div>
			</aside>
		</div>
	{:else}
		{#if list}<div class="list">{@render list()}</div>{/if}
		{#if !beside}
			{#if viewport.mobile}
				<NeoDialog bind:open modal full filled placement="center" elevation={2} padding="0" borderless slide={!viewport.reducedMotion} onclose={closed}>
					<div class="sheet mobile">{@render children()}</div>
				</NeoDialog>
			{:else}
				<NeoDrawer bind:open filled placement="right" width="{width}px" elevation={2} padding="0" rounded="md" slide={!viewport.reducedMotion} onclose={closed}>
					<div class="sheet" style:width="{width}px">{@render children()}</div>
				</NeoDrawer>
			{/if}
		{/if}
	{/if}
</div>

<style>
	.sheetwrap {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-5);
		min-width: 0;
	}
	.head {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-4);
		min-width: 0;
	}
	.split {
		display: flex;
		gap: var(--nebu-space-5);
		align-items: flex-start;
		min-width: 0;
	}
	.split > .list {
		flex: 1 1 0;
	}
	.list {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-5);
		min-width: 0;
	}
	.pane {
		position: sticky;
		top: calc(var(--nebu-top, 61px) + var(--nebu-space-4));
		display: flex;
		flex: 0 0 clamp(480px, 50%, 900px);
		flex-direction: column;
		min-width: 0;
	}
	.panel {
		--nebu-sheet-pad: var(--nebu-space-5);

		display: flex;
		flex-direction: column;
		min-width: 0;
		max-height: calc(100dvh - var(--nebu-top, 61px) - 2 * var(--nebu-space-4));
		padding: var(--nebu-sheet-pad);
		overflow: auto;
		background: var(--neo-background-color);
		border-radius: var(--neo-border-radius-md);
		box-shadow: var(--neo-box-shadow-raised-2), var(--nebu-machined);
	}
	.sheet {
		--nebu-sheet-pad: var(--nebu-space-5);

		display: flex;
		flex-direction: column;
		height: 100%;
		min-height: 0;
		padding: var(--nebu-sheet-pad);
		overflow: auto;
	}
	.sheet.mobile {
		--nebu-sheet-pad: var(--nebu-space-4);

		width: 100vw;
		height: 100dvh;
	}
</style>
