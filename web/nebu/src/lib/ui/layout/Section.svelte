<script lang="ts">
	import type { Snippet } from 'svelte';
	import InfoTip from '#lib/ui/InfoTip.svelte';

	// A titled block inside a card or panel with quiet tools and one info line
	let {
		title,
		info,
		tools,
		children,
		gap = 0,
		level = 2
	}: {
		title: string;
		info?: string;
		tools?: Snippet;
		children: Snippet;
		gap?: 0 | 4 | 8 | 12 | 16;
		level?: 2 | 3;
	} = $props();
</script>

<section class="section" aria-label={title}>
	<div class="head">
		<div class="heading">
			<svelte:element this={`h${level}`} class="title">{title}</svelte:element>
			{#if info}<InfoTip text={info} />{/if}
		</div>
		{#if tools}<div class="tools">{@render tools()}</div>{/if}
	</div>
	<div class="body" style:gap="{gap}px">
		{@render children()}
	</div>
</section>

<style>
	.section {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-3);
		min-width: 0;
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-2) var(--nebu-space-3);
		align-items: center;
		justify-content: space-between;
		min-height: 28px;
	}
	.heading {
		display: flex;
		flex: 0 0 auto;
		gap: var(--nebu-space-2);
		align-items: center;
		min-width: 0;
	}
	.title {
		color: var(--neo-text-color);
	}
	.tools {
		display: flex;
		flex: 0 0 auto;
		gap: var(--nebu-space-1);
		align-items: center;
	}
	.body {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
</style>
