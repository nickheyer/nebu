<script lang="ts">
	import type { Snippet } from 'svelte';

	let { children, padding = 12, class: cls = '', maxHeight }: { children: Snippet; padding?: 0 | 8 | 12 | 16; class?: string; maxHeight?: string } = $props();
</script>

<div class="screen {cls}" style:padding="{padding}px" style:max-height={maxHeight}>
	{@render children()}
</div>

<style>
	.screen {
		min-width: 0;
		overflow: auto;
	}
</style>
