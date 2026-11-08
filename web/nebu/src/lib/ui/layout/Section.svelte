<script lang="ts">
	import type { Snippet } from 'svelte';
	import InfoTip from '#lib/ui/InfoTip.svelte';

	// A titled block of a page. The heading is an h2 and sits a clear step under the page title.
	// `tools` holds quiet controls for the section, never the page's primary action.
	// Anything the title does not say goes in `info`, one line behind the (i).
	let {
		title,
		info,
		count,
		tools,
		children,
		gap = 0,
		level = 2
	}: {
		title: string;
		info?: string;
		count?: number | string;
		tools?: Snippet;
		children: Snippet;
		gap?: 0 | 4 | 8 | 12 | 16;
		// h3 for a section nested inside another
		level?: 2 | 3;
	} = $props();
</script>

<section class="section" aria-label={title}>
	<div class="head">
		<div class="heading">
			<svelte:element this={`h${level}`} class="title">{title}</svelte:element>
			{#if count !== undefined}<span class="count">{count}</span>{/if}
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
	/* The tools drop under the title when the two no longer share the width */
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
		align-items: baseline;
		min-width: 0;
	}
	.title {
		color: var(--neo-text-color);
	}
	.count {
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
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
