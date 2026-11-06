<script lang="ts">
	import type { Snippet } from 'svelte';

	let { gap = 16, children, class: cls = '' }: { gap?: 0 | 4 | 8 | 12 | 16 | 24 | 32; children: Snippet; class?: string } = $props();
</script>

<div class="stack {cls}" style:gap="{gap}px">
	{@render children()}
</div>

<style>
	.stack {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
</style>
