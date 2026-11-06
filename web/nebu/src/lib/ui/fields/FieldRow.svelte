<script lang="ts">
	import type { Snippet } from 'svelte';
	import { viewport } from '#lib/state/viewport.svelte.js';

	let { children }: { children: Snippet } = $props();

	let width = $state(0);
	// One field per row on mobile and inside any container too narrow for two
	const single = $derived(viewport.mobile || (width > 0 && width < 480));
</script>

<div class="fieldrow" class:single bind:clientWidth={width}>
	{@render children()}
</div>

<style>
	.fieldrow {
		display: flex;
		gap: 16px;
		align-items: flex-start;
		min-width: 0;
	}
	.fieldrow > :global(*) {
		flex: 1 1 0;
		min-width: 0;
	}
	.fieldrow.single {
		flex-direction: column;
		align-items: stretch;
	}
	.fieldrow.single > :global(*) {
		flex: 0 0 auto;
	}
</style>
