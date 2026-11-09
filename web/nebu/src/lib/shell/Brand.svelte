<script lang="ts">
	import Str from '#lib/ui/Str.svelte';

	// The mark beside the name of the instance it opens
	let { name = '', size = 24, href = '/' }: { name?: string; size?: 24 | 28 | 40; href?: string } = $props();
</script>

<a class="brand" {href} aria-label={name ? `${name} home` : 'Home'}>
	<img class="mark" src="/favicon.svg" alt="" width={size} height={size} />
	{#if name}<span class="name"><Str value={name} kind="name" weight={500} width="100%" /></span>{/if}
</a>

<style>
	.brand {
		display: inline-flex;
		gap: 10px;
		align-items: center;
		min-width: 0;
		color: var(--neo-text-color);
		text-decoration: none;
	}
	.mark {
		display: block;
		flex: 0 0 auto;
	}
	.name {
		display: inline-flex;
		min-width: 0;
		font-size: var(--nebu-text-body);
		letter-spacing: 0.01em;
	}
</style>
