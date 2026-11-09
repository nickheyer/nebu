<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';

	let { next, loading = false, onmore }: { next: string; loading?: boolean; onmore: () => void } = $props();
</script>

{#if next}
	<div class="more">
		<NeoButton elevation={2} {loading} onclick={onmore}>Load more</NeoButton>
	</div>
{/if}

<style>
	.more {
		display: flex;
		justify-content: center;
		padding: 12px 0;
	}
</style>
