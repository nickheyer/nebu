<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoCloseButton } from '#lib/neo/buttons/index.js';
	import Str, { type StrKind } from '#lib/ui/Str.svelte';

	let {
		title,
		subtitle,
		onclose,
		children,
		tools,
		kind = 'name'
	}: { title: string; subtitle?: string; onclose?: () => void; children?: Snippet; tools?: Snippet; kind?: StrKind } = $props();
</script>

<div class="head">
	<div class="titles">
		<h2 class="title" class:wide={kind === 'repo' || kind === 'path' || kind === 'message'}><Str value={title} {kind} tip={title} weight={500} width="100%" /></h2>
		{#if subtitle}<span class="secondary ellipsis">{subtitle}</span>{/if}
	</div>
	{#if children}{@render children()}{/if}
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
	.title {
		display: flex;
		min-width: 0;
		max-width: 24ch;
	}
	.title.wide {
		max-width: 40ch;
	}
</style>
