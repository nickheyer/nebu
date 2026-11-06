<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoCard } from '#lib/neo/cards/index.js';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';
	import { NeoDrawer } from '#lib/neo/floating/drawer/index.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import Split from '#lib/ui/layout/Split.svelte';

	// A drawer from the right at standard width, full screen on mobile, the right pane of a split on ultrawide
	let {
		open = $bindable(false),
		onclose,
		list,
		children,
		empty = 'Nothing selected',
		emptyAction,
		emptyHref,
		width = 480
	}: { open?: boolean; onclose?: () => void; list?: Snippet; children: Snippet; empty?: string; emptyAction?: string; emptyHref?: string; width?: number } = $props();

	function closed() {
		open = false;
		onclose?.();
	}
</script>

{#if list && viewport.ultrawide}
	<Split ratio="2/5">
		{#snippet left()}{@render list()}{/snippet}
		{#snippet right()}
			<div class="pane">
				<NeoCard elevation={2} rounded="md" spacing="16px" width="100%">
					{#if open}
						<div class="panel">{@render children()}</div>
					{:else}
						<Empty text={empty} action={emptyAction} href={emptyHref} />
					{/if}
				</NeoCard>
			</div>
		{/snippet}
	</Split>
{:else}
	{@render list?.()}
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

<style>
	.pane {
		position: sticky;
		top: 24px;
		display: flex;
		min-width: 0;
	}
	.pane :global(.neo-card) {
		max-height: calc(100dvh - 96px);
		overflow: auto;
	}
	.panel {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.sheet {
		display: flex;
		flex-direction: column;
		height: 100%;
		min-height: 0;
		padding: 16px;
		overflow: auto;
	}
	.sheet.mobile {
		width: 100vw;
		min-height: 100dvh;
	}
</style>
