<script lang="ts">
	import type { Snippet } from 'svelte';
	import { viewport } from '#lib/state/viewport.svelte.js';

	// Wide rows hold controls such as durations with their unit buttons, so they fold sooner
	let { children, wide = false }: { children: Snippet; wide?: boolean } = $props();

	let width = $state(0);
	// Two fields per row where the container allows, else one
	const single = $derived(viewport.mobile || (width > 0 && width < (wide ? 720 : 480)));
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
