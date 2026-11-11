<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoCloseButton } from '#lib/neo/buttons/index.js';
	import Dot from '#lib/ui/Dot.svelte';
	import OriginLink from '#lib/ui/OriginLink.svelte';
	import Str, { type StrKind } from '#lib/ui/Str.svelte';
	import type { DotShape, Lamp } from '#lib/ui/status.js';

	// A panel's title row with a lamp before the title, the state in words under it, the title a link when given one
	let {
		title,
		subtitle,
		href,
		lamp,
		shape = 'filled',
		state,
		onclose,
		tools,
		kind = 'name'
	}: {
		title: string;
		subtitle?: string;
		href?: string;
		lamp?: Lamp;
		shape?: DotShape;
		state?: string;
		onclose?: () => void;
		tools?: Snippet;
		kind?: StrKind;
	} = $props();

	const under = $derived([state, subtitle].filter(Boolean).join(' · '));
</script>

<div class="head">
	<div class="titles">
		<div class="line">
			{#if lamp}<Dot {lamp} {shape} label={state} />{/if}
			{#if href}
				<h2 class="title"><OriginLink {href}>{title}</OriginLink></h2>
			{:else}
				<h2 class="title"><Str value={title} {kind} weight={500} width="100%" /></h2>
			{/if}
		</div>
		{#if under}<span class="secondary under" class:lit={lamp !== undefined}>{under}</span>{/if}
	</div>
	<span class="fill"></span>
	{#if tools}{@render tools()}{/if}
	{#if onclose}
		<NeoCloseButton text aria-label="Close" onclick={onclose} />
	{/if}
</div>

<style>
	.head {
		display: flex;
		gap: 8px;
		align-items: center;
		min-height: 36px;
		margin-bottom: 12px;
	}
	/* The close glyph sits on the panel's edge, its button box reaching past it */
	.head > :global(.neo-button.neo-empty:last-child) {
		margin-right: -7.5px;
	}
	.titles {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.line {
		display: flex;
		gap: 8px;
		align-items: center;
		min-width: 0;
	}
	.title {
		display: flex;
		min-width: 0;
	}
	.under {
		min-width: 0;
		overflow-wrap: anywhere;
	}
	/* The words under a lit title start where the title does, past the lamp */
	.under.lit {
		padding-left: 16px;
	}
</style>
