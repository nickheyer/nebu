<script lang="ts">
	import type { Snippet } from 'svelte';
	import { leaveList } from '#lib/ui/list.svelte.js';

	// Holds content nested in a row, its own rows laying out apart from the list around it
	let { children }: { children: Snippet } = $props();

	leaveList();
</script>

{@render children()}
