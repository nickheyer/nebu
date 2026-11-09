<script lang="ts">
	import type { Snippet } from 'svelte';

	let {
		gap = 12,
		align = 'center',
		justify = 'flex-start',
		wrap = true,
		children,
		class: cls = ''
	}: { gap?: 4 | 8 | 12 | 16 | 24; align?: 'center' | 'flex-start' | 'flex-end' | 'baseline' | 'stretch'; justify?: 'flex-start' | 'flex-end' | 'space-between' | 'center'; wrap?: boolean; children: Snippet; class?: string } = $props();
</script>

<div class="row {cls}" style:gap="{gap}px" style:align-items={align} style:justify-content={justify} style:flex-wrap={wrap ? 'wrap' : 'nowrap'}>
	{@render children()}
</div>

<style>
	.row {
		display: flex;
		min-width: 0;
	}
</style>
