<script lang="ts">
	import Str from '#lib/ui/Str.svelte';

	// The mark alone, or beside the product's name over the name of the instance it opens
	let { name = '', size = 24, href = '/' }: { name?: string; size?: 24 | 28 | 32 | 40; href?: string } = $props();
</script>

<a class="brand" {href} aria-label={name ? `${name} home` : 'Home'}>
	<img class="mark" src="/favicon.svg" alt="" width={size} height={size} />
	{#if name}
		<span class="words">
			<span class="product">nebu</span>
			<span class="host"><Str value={name} kind="name" color="muted" width="100%" clip /></span>
		</span>
	{/if}
</a>

<style>
	.brand {
		display: inline-flex;
		gap: 12px;
		align-items: center;
		min-width: 0;
		color: var(--neo-text-color);
		text-decoration: none;
	}
	.mark {
		display: block;
		flex: 0 0 auto;
	}
	.words {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.product {
		font-size: 17px;
		font-weight: 600;
		line-height: 20px;
		letter-spacing: 0.02em;
	}
	.host {
		display: flex;
		min-width: 0;
		font-size: var(--nebu-text-caption);
		line-height: 16px;
	}
</style>
