<script lang="ts">
	import { num } from '#lib/format.js';
	import Screen from '#lib/ui/Screen.svelte';

	// Characters shown before the rest folds behind a count
	const cut = 1000;

	// Text on a screen as written, a long one cut until the rest is asked for
	let { text, maxHeight = '24rem' }: { text: string; maxHeight?: string } = $props();

	let whole = $state(false);
	const shown = $derived(whole || text.length <= cut ? text : text.slice(0, cut));
</script>

<Screen padding={8} {maxHeight}>
	<div class="lines">{shown}{#if shown.length < text.length}<button type="button" class="more dim" onclick={() => (whole = true)}>{num(text.length - shown.length)} more</button>{/if}</div>
</Screen>

<style>
	.lines {
		font-family: var(--nebu-font-mono);
		font-size: 13px;
		line-height: 1.5;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.more {
		padding: 0 1ch;
		font: inherit;
		cursor: pointer;
		background: none;
		border: 0;
		text-decoration: underline;
	}
</style>
