<script lang="ts">
	import type { Snippet } from 'svelte';

	// One child of a Row with the least and most width it takes, the row wrapping once the minimums no longer fit
	let { min = '10rem', max = 'none', grow = true, push = false, children }: { min?: string; max?: string; grow?: boolean; push?: boolean; children: Snippet } = $props();
</script>

<div class="cell" class:push style:flex={grow ? `1 1 ${min}` : '0 0 auto'} style:min-width={grow ? min : undefined} style:max-width={max}>
	{@render children()}
</div>

<style>
	.cell {
		display: flex;
		align-items: center;
		min-width: 0;
	}
	.cell.push {
		margin-left: auto;
	}
	.cell > :global(*) {
		flex: 1 1 auto;
		min-width: 0;
	}
</style>
