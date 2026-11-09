<script lang="ts">
	import type { Snippet } from 'svelte';

	// A row of compact controls over a list
	let { children }: { children: Snippet } = $props();
</script>

<div class="toolbar">{@render children()}</div>

<style>
	.toolbar {
		--neo-input-padding: 0.375rem 0.75rem;

		display: flex;
		flex: 1 1 auto;
		flex-wrap: wrap;
		gap: var(--nebu-space-2);
		align-items: center;
		min-width: 0;
	}
	.toolbar > :global(.grow) {
		display: flex;
		flex: 1 1 16rem;
		min-width: 0;
	}
	.toolbar > :global(.grow > *) {
		flex: 1 1 auto;
		min-width: 0;
	}
	.toolbar > :global(.push) {
		margin-left: auto;
	}
</style>
