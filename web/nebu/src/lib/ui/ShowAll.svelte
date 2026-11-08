<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';

	let { total, shown, open = $bindable(false), label = 'Show all' }: { total: number; shown: number; open?: boolean; label?: string } = $props();
</script>

{#if total > shown || open}
	<div class="more">
		<NeoButton text onclick={() => (open = !open)}>{open ? 'Show fewer' : label}{#if !open}<span class="count">{total}</span>{/if}</NeoButton>
	</div>
{/if}

<style>
	.more {
		display: flex;
		justify-content: center;
		padding: 4px 0;
	}
	.count {
		margin-left: 6px;
		font-size: var(--nebu-text-caption);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
	}
</style>
