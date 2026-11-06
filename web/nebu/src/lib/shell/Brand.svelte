<script lang="ts">
	let { collapsed = false }: { collapsed?: boolean } = $props();
</script>

<a class="brand" href="/" aria-label="nebu">
	<img class="mark" src="/favicon.svg" alt="" width="24" height="24" />
	{#if !collapsed}<span class="word">nebu</span>{/if}
</a>

<style>
	.brand {
		display: inline-flex;
		gap: 8px;
		align-items: center;
		color: var(--neo-text-color);
		text-decoration: none;
	}
	.mark {
		display: block;
		width: 24px;
		height: 24px;
	}
	.word {
		font-size: 17px;
		font-weight: 600;
		line-height: 1;
	}
</style>
