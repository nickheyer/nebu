<script lang="ts">
	let { planned = false }: { planned?: boolean } = $props();
</script>

<div class="legend">
	<span class="item"><span class="swatch instance"></span>instances</span>
	<span class="item"><span class="swatch reservation"></span>reservations</span>
	<span class="item"><span class="swatch other"></span>other</span>
	<span class="item"><span class="swatch free"></span>free</span>
	{#if planned}<span class="item"><span class="swatch planned"></span>planned</span>{/if}
</div>

<style>
	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: 16px;
		font-size: 12px;
		color: var(--nebu-text-muted);
	}
	.item {
		display: inline-flex;
		gap: 6px;
		align-items: center;
	}
	.swatch {
		display: inline-block;
		width: 14px;
		height: 8px;
		border-radius: 2px;
	}
	.instance {
		background: var(--neo-color-primary);
	}
	.reservation {
		background: repeating-linear-gradient(135deg, var(--neo-color-primary) 0 3px, transparent 3px 6px);
	}
	.planned {
		background: repeating-linear-gradient(135deg, var(--nebu-steel) 0 3px, transparent 3px 6px);
	}
	.other {
		background: var(--nebu-text-muted);
	}
	.free {
		background: var(--neo-background-color-secondary);
		box-shadow: var(--neo-box-shadow-inset-1);
	}
</style>
