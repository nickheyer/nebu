<script lang="ts">
	import type { Snippet } from 'svelte';
	import InfoTip from '#lib/ui/InfoTip.svelte';

	// A raised block of a page with a title row, quiet tools, a body and an optional footer
	let {
		title,
		info,
		tools,
		children,
		footer,
		flush = false,
		grow = false,
		// Whether the body scrolls inside a bounded card
		scroll = false,
		pad = 'normal'
	}: {
		title?: string;
		info?: string;
		tools?: Snippet;
		children: Snippet;
		footer?: Snippet;
		flush?: boolean;
		grow?: boolean;
		scroll?: boolean;
		pad?: 'normal' | 'roomy';
	} = $props();
</script>

<section class="card" class:grow class:scroll class:roomy={pad === 'roomy'} aria-label={title}>
	{#if title !== undefined || tools}
		<header class="head" class:bare={title === undefined}>
			{#if title !== undefined}
				<div class="heading">
					<h2 class="title">{title}</h2>
					{#if info}<InfoTip text={info} />{/if}
				</div>
			{/if}
			{#if tools}<div class="tools">{@render tools()}</div>{/if}
		</header>
	{/if}
	<div class="body" class:flush class:scroll class:headed={title !== undefined || tools !== undefined}>
		{@render children()}
	</div>
	{#if footer}<footer class="foot">{@render footer()}</footer>{/if}
</section>

<style>
	.card {
		--nebu-card-pad: var(--nebu-card-pad-base, var(--nebu-space-5));
		--nebu-row-inset: var(--nebu-card-pad);

		display: flex;
		flex-direction: column;
		min-width: 0;
		background: var(--neo-background-color);
		border-radius: var(--neo-border-radius-md);
		box-shadow: var(--neo-box-shadow-raised-2), var(--nebu-machined);
		animation: nebu-reveal 320ms var(--nebu-settle) backwards;
		animation-delay: calc(var(--nebu-settle-step, 0ms) + var(--nebu-settle-shift, 0ms) + var(--nebu-settle-rank, 0ms));
	}
	.card.roomy {
		--nebu-card-pad: var(--nebu-space-6);
	}
	.card.grow {
		flex: 1 1 auto;
	}
	.card.scroll {
		min-height: 0;
	}
	.body.scroll {
		min-height: 0;
		overflow: auto;
		overscroll-behavior: contain;
	}
	.body.scroll :global(.listhead) {
		position: sticky;
		top: 0;
		z-index: 1;
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-2) var(--nebu-space-3);
		align-items: center;
		justify-content: space-between;
		min-height: 56px;
		padding: var(--nebu-space-3) var(--nebu-card-pad);
	}
	/* A card without a title gives its whole head to the tools */
	.head.bare > .tools {
		flex: 1 1 auto;
		justify-content: flex-start;
	}
	.heading {
		display: flex;
		flex: 0 1 auto;
		gap: var(--nebu-space-2);
		align-items: center;
		min-width: 0;
	}
	.title {
		font-size: var(--nebu-text-heading);
		white-space: nowrap;
	}
	.tools {
		--neo-input-padding: 0.375rem 0.75rem;

		display: flex;
		flex: 0 1 auto;
		flex-wrap: wrap;
		gap: var(--nebu-space-2);
		align-items: center;
		justify-content: flex-end;
		min-width: 0;
	}
	.body {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		gap: var(--nebu-space-4);
		min-width: 0;
		padding: var(--nebu-card-pad);
	}
	.body.headed {
		padding-top: 0;
	}
	.body.flush {
		gap: 0;
		padding: 0 0 var(--nebu-space-2);
	}
	.body.flush:not(.headed) {
		padding-top: var(--nebu-space-2);
	}
	.foot {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		padding: var(--nebu-space-3) var(--nebu-card-pad);
		border-top: 1px solid var(--nebu-border);
	}
</style>
