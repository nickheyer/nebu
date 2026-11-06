<script lang="ts">
	import type { Snippet } from 'svelte';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import BottomBar from '#lib/shell/BottomBar.svelte';
	import Rail from '#lib/shell/Rail.svelte';
	import TopBar from '#lib/shell/TopBar.svelte';

	let { children }: { children: Snippet } = $props();
</script>

<div class="shell" class:mobile={viewport.mobile} class:ultrawide={viewport.ultrawide}>
	{#if !viewport.mobile}
		<Rail />
	{/if}
	<div class="body">
		<TopBar />
		<main class="content">
			<div class="page">
				{@render children()}
			</div>
		</main>
		{#if viewport.mobile}
			<BottomBar />
		{/if}
	</div>
</div>

<style>
	.shell {
		display: flex;
		min-height: 100dvh;
	}
	.body {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		min-width: 0;
		min-height: 100dvh;
	}
	.content {
		flex: 1 1 auto;
		min-height: 0;
		padding: 24px;
	}
	.page {
		max-width: 1440px;
		margin: 0 auto;
	}
	.ultrawide .page {
		max-width: 1920px;
	}
	.mobile .content {
		padding: 16px;
	}
</style>
