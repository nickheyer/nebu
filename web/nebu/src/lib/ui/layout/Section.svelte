<script lang="ts">
	import type { Snippet } from 'svelte';
	import InfoTip from '#lib/ui/InfoTip.svelte';

	let {
		title,
		info,
		count,
		tools,
		children,
		gap = 8
	}: { title: string; info?: string; count?: number | string; tools?: Snippet; children: Snippet; gap?: 0 | 4 | 8 | 12 | 16 } = $props();
</script>

<section class="section">
	<div class="head">
		<h2>{title}</h2>
		{#if count !== undefined}<span class="count">{count}</span>{/if}
		{#if info}<InfoTip text={info} />{/if}
		<span class="fill"></span>
		{#if tools}{@render tools()}{/if}
	</div>
	<div class="body" style:gap="{gap}px">
		{@render children()}
	</div>
</section>

<style>
	.section {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
	}
	.head {
		display: flex;
		gap: 8px;
		align-items: center;
		min-height: 32px;
	}
	.count {
		font-size: 13px;
		color: var(--nebu-text-muted);
	}
	.body {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
</style>
