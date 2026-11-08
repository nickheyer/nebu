<script lang="ts">
	import type { Snippet } from 'svelte';
	import { leaveList } from '#lib/ui/list.svelte.js';

	// Lays nested rows out apart from the list around them
	let { children }: { children: Snippet } = $props();

	leaveList();
</script>

{@render children()}
