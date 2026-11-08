<script lang="ts">
	import type { Snippet } from 'svelte';
	import { chrome, type HeaderTab } from '#lib/shell/chrome.svelte.js';

	// Declares the page's title, tabs and actions for the top bar, rendering nothing in place
	let {
		title,
		back,
		meta,
		actions,
		tabs,
		active,
		ontab
	}: {
		title?: string;
		back?: { label: string; href: string };
		meta?: Snippet;
		actions?: Snippet;
		tabs?: HeaderTab[];
		active?: string;
		ontab?: (id: string) => void;
	} = $props();

	$effect(() => {
		const id = chrome.set({ title, back, meta, actions, tabs, active, ontab });
		return () => chrome.clear(id);
	});
</script>
