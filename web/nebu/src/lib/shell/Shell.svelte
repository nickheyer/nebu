<script lang="ts">
	import type { Snippet } from 'svelte';
	import { useNeoThemeContext } from '#lib/neo/providers/neo-theme-provider-context.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import { motion } from '#lib/ui/motion.svelte.js';
	import BottomBar from '#lib/shell/BottomBar.svelte';
	import Rail from '#lib/shell/Rail.svelte';
	import TopBar from '#lib/shell/TopBar.svelte';

	let { children }: { children: Snippet } = $props();

	// The appearance toggle and the motion preference decide whether the deck animates
	const theme = useNeoThemeContext();
	$effect(() => {
		motion.enabled = theme.transition !== 'none' && !viewport.reducedMotion;
	});
</script>

<div class="shell" class:mobile={viewport.mobile} data-motion={motion.enabled ? 'on' : 'off'}>
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
	/* Blocks settle in page order, the top one first */
	.page > :global(:nth-child(2)) {
		--nebu-settle-step: 60ms;
	}
	.page > :global(:nth-child(3)) {
		--nebu-settle-step: 120ms;
	}
	.page > :global(:nth-child(4)) {
		--nebu-settle-step: 180ms;
	}
	.page > :global(:nth-child(n + 5)) {
		--nebu-settle-step: 240ms;
	}
</style>
