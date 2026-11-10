<script lang="ts">
	import { chrome, type HeaderTab } from '#lib/shell/chrome.svelte.js';

	// Declares the page's title, where it comes from and its tabs for the top bar
	let {
		title,
		back,
		origin,
		tabs,
		active,
		ontab
	}: {
		title?: string;
		back?: { label: string; href: string };
		origin?: { label: string; href: string };
		tabs?: HeaderTab[];
		active?: string;
		ontab?: (id: string) => void;
	} = $props();

	$effect(() => {
		const id = chrome.set({ title, back, origin, tabs, active, ontab });
		return () => chrome.clear(id);
	});
</script>
