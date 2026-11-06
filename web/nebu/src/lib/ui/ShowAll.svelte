<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';

	let { total, shown, open = $bindable(false), label = 'Show all' }: { total: number; shown: number; open?: boolean; label?: string } = $props();
</script>

{#if total > shown || open}
	<div class="more">
		<NeoButton text onclick={() => (open = !open)}>{open ? 'Show fewer' : `${label} (${total})`}</NeoButton>
	</div>
{/if}

<style>
	.more {
		display: flex;
		justify-content: center;
		padding: 4px 0;
	}
</style>
