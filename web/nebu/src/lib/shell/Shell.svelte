<script lang="ts">
	import type { Snippet } from 'svelte';
	import { innerWidth } from 'svelte/reactivity/window';
	import { session } from '#lib/auth/session.svelte.js';
	import { prefs } from '#lib/state/prefs.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import { motion } from '#lib/ui/motion.svelte.js';
	import BottomBar from '#lib/shell/BottomBar.svelte';
	import { immersion } from '#lib/shell/chrome.svelte.js';
	import Morpheus from '#lib/shell/Morpheus.svelte';
	import { morpheus, span } from '#lib/shell/morpheus.svelte.js';
	import Rail from '#lib/shell/Rail.svelte';
	import TopBar from '#lib/shell/TopBar.svelte';

	let { children }: { children: Snippet } = $props();

	const rail = $derived(viewport.mobile || immersion.on ? 0 : prefs.railCollapsed ? 68 : 232);
	const room = $derived((innerWidth.current ?? 0) - rail - span.page);
	const docked = $derived(!viewport.mobile && span.min <= room);
</script>

<!-- A page that takes the whole window keeps the shell mounted and out of view, so leaving it changes nothing else -->
<div class="shell" class:mobile={viewport.mobile} class:collapsed={prefs.railCollapsed} class:immersive={immersion.on} data-motion={motion.enabled ? 'on' : 'off'} style:--nebu-rail-width="{rail}px">
	{#if !viewport.mobile}
		<div class="chrome"><Rail /></div>
	{/if}
	<div class="body">
		<div class="chrome"><TopBar /></div>
		<main class="content">
			<div class="page">
				{@render children()}
			</div>
		</main>
		{#if viewport.mobile}
			<div class="chrome foot"><BottomBar /></div>
		{/if}
	</div>
	{#if session.reads && morpheus.here}
		<div class="chrome"><Morpheus {docked} {room} /></div>
	{/if}
</div>

<style>
	.shell {
		--nebu-gutter: var(--nebu-space-6);
		--nebu-pad-top: var(--nebu-space-5);
		--nebu-pad-bottom: var(--nebu-space-6);
		--nebu-bottom: 0px;

		display: flex;
		min-height: 100dvh;
	}
	/* The wrappers of the rail and the bars take no box of their own */
	.chrome {
		display: contents;
	}
	.shell.immersive .chrome {
		display: none;
	}
	.shell.immersive .page {
		max-width: none;
	}
	.shell.mobile {
		--nebu-bottom: 56px;
		--nebu-card-pad-base: var(--nebu-space-4);
		--nebu-gutter: var(--nebu-space-4);
		--nebu-pad-top: var(--nebu-space-4);
		--nebu-pad-bottom: var(--nebu-space-5);
	}
	/* A page holding the whole window sets every measure of the shell around it to nothing, on any width */
	.shell.immersive {
		--nebu-bottom: 0px;
		--nebu-top: 0px;
		--nebu-gutter: 0px;
		--nebu-pad-top: 0px;
		--nebu-pad-bottom: 0px;
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
