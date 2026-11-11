<script lang="ts">
	import { contextTones } from '#lib/pages/models/context.svelte.js';
</script>

<div class="legend">
	{#each contextTones as t (t.lamp)}
		<span class="item"><span class="ring {t.lamp}" aria-hidden="true"></span>{t.label}</span>
	{/each}
</div>

<style>
	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-1) var(--nebu-space-4);
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-muted);
	}
	.item {
		display: inline-flex;
		gap: 6px;
		align-items: center;
	}
	.ring {
		box-sizing: border-box;
		width: 10px;
		height: 10px;
		border: 2px solid var(--tone);
		border-radius: 50%;
	}
	.success {
		--tone: var(--neo-color-success);
	}
	.warning {
		--tone: var(--neo-color-warning);
	}
	.error {
		--tone: var(--neo-color-error);
	}
</style>
