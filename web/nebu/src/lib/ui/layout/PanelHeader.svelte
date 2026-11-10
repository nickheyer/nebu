<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoCloseButton } from '#lib/neo/buttons/index.js';
	import OriginLink from '#lib/ui/OriginLink.svelte';
	import Str, { type StrKind } from '#lib/ui/Str.svelte';

	// A panel's title row, the title a link to where the record comes from when an origin is given
	let {
		title,
		subtitle,
		origin,
		onclose,
		children,
		tools,
		kind = 'name'
	}: { title: string; subtitle?: string; origin?: { label: string; href: string }; onclose?: () => void; children?: Snippet; tools?: Snippet; kind?: StrKind } = $props();
</script>

<div class="head">
	<div class="titles">
		<div class="line">
			{#if origin}
				<h2 class="title"><OriginLink href={origin.href} from={origin.label}>{title}</OriginLink></h2>
			{:else}
				<h2 class="title"><Str value={title} {kind} tip={title} weight={500} width="100%" /></h2>
			{/if}
			{#if children}{@render children()}{/if}
		</div>
		{#if subtitle}<span class="secondary subtitle">{subtitle}</span>{/if}
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
	.titles {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	/* Pills and marks sit beside the title itself, not the title block */
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
	.subtitle {
		min-width: 0;
		overflow-wrap: anywhere;
	}
</style>
