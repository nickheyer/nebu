<script lang="ts">
	import { NeoSkeletonText } from '#lib/neo/skeletons/index.js';

	// Rows of bones in the row anatomy's layout, a dot, a name, a phrase and a fact
	let { rows = 3, height = 44 }: { rows?: number; height?: number } = $props();
</script>

<div class="skeleton" aria-busy="true" aria-label="Loading">
	{#each { length: rows } as _, i (i)}
		<div class="line" style:height="{height}px">
			<NeoSkeletonText lines={1} width="8px" height="8px" />
			<NeoSkeletonText lines={1} width="24ch" />
			<NeoSkeletonText lines={1} width="40ch" />
			<span class="fill"></span>
			<NeoSkeletonText lines={1} width="8ch" />
		</div>
	{/each}
</div>

<style>
	.skeleton {
		display: flex;
		flex-direction: column;
	}
	.line {
		display: flex;
		gap: 12px;
		align-items: center;
		padding: 4px 8px;
		border-bottom: 1px solid var(--nebu-border);
	}
	.line > :global(*) {
		flex: 0 0 auto;
	}
</style>
