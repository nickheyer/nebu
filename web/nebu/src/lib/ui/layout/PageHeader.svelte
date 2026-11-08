<script lang="ts">
	import { chrome, type HeaderTab } from '#lib/shell/chrome.svelte.js';

	// Declares the page's title, info line and tabs for the top bar
	let {
		title,
		back,
		info,
		tabs,
		active,
		ontab
	}: {
		title?: string;
		back?: { label: string; href: string };
		info?: string;
		tabs?: HeaderTab[];
		active?: string;
		ontab?: (id: string) => void;
	} = $props();

	$effect(() => {
		const id = chrome.set({ title, back, info, tabs, active, ontab });
		return () => chrome.clear(id);
	});
</script>
