<script lang="ts">
	import type { Snippet } from 'svelte';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import BottomBar from '#lib/shell/BottomBar.svelte';
	import Rail from '#lib/shell/Rail.svelte';
	import TopBar from '#lib/shell/TopBar.svelte';

	let { children }: { children: Snippet } = $props();
</script>

<div class="shell" class:mobile={viewport.mobile}>
	{#if !viewport.mobile}
		<Rail />
	{/if}
	<div class="body">
		<TopBar />
		<main class="content">
			<div class="page">
				{@render children()}
			</div>
		</main>
		{#if viewport.mobile}
			<BottomBar />
		{/if}
	</div>
</div>

<style>
	/* The chrome around a page, read by pages that fill the viewport height */
	.shell {
		--nebu-gutter: var(--nebu-space-6);
		--nebu-pad-top: var(--nebu-space-5);
		--nebu-pad-bottom: var(--nebu-space-6);

		display: flex;
		min-height: 100dvh;
	}
	.shell.mobile {
		--nebu-card-pad-base: var(--nebu-space-4);
		--nebu-gutter: var(--nebu-space-4);
		--nebu-pad-top: var(--nebu-space-4);
		--nebu-pad-bottom: var(--nebu-space-5);
	}
	.body {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		min-width: 0;
		min-height: 100dvh;
	}
	.content {
		flex: 1 1 auto;
		min-height: 0;
		padding: var(--nebu-pad-top) var(--nebu-gutter) var(--nebu-pad-bottom);
	}
	/* Every page shares the top bar's frame so titles, tabs and content start on one edge */
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-5);
		max-width: calc(var(--nebu-frame-max) - 2 * var(--nebu-gutter));
		margin: 0 auto;
	}
	.mobile .page {
		gap: var(--nebu-space-4);
	}
</style>
