<script lang="ts">
	import type { Snippet } from 'svelte';
	import { viewport } from '#lib/state/viewport.svelte.js';

	// Two stacks in a fixed ratio, one stack on mobile
	let { ratio = '2/5', left, right, gap = 24 }: { ratio?: '2/5' | '3/5' | '1/3' | '2/3'; left: Snippet; right: Snippet; gap?: 16 | 24 | 32 } = $props();

	const weights = $derived({ '2/5': [2, 3], '3/5': [3, 2], '1/3': [1, 2], '2/3': [2, 1] }[ratio]);
</script>

{#if viewport.mobile}
	<div class="stacked" style:gap="{gap}px">
		{@render left()}
		{@render right()}
	</div>
{:else}
	<div class="split" style:gap="{gap}px">
		<div class="pane" style:flex="{weights[0]} 1 0">{@render left()}</div>
		<div class="pane" style:flex="{weights[1]} 1 0">{@render right()}</div>
	</div>
{/if}

<style>
	.split {
		display: flex;
		align-items: flex-start;
		min-width: 0;
	}
	.pane {
		display: flex;
		flex-direction: column;
		gap: 24px;
		min-width: 0;
	}
	.stacked {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
</style>
