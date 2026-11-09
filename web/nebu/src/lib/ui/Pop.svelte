<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoTooltip } from '#lib/neo/floating/tooltips/index.js';
	import type { NeoTooltipPlacement } from '#lib/neo/floating/common/neo-placement.model.js';

	let {
		open = $bindable(false),
		placement = 'bottom-start',
		width,
		content,
		children
	}: { open?: boolean; placement?: NeoTooltipPlacement; width?: string; content: Snippet<[() => void]>; children: Snippet } = $props();

	function close() {
		open = false;
	}
</script>

<NeoTooltip bind:open {placement} elevation={2} filled portal flipOptions={{ fallbackAxisSideDirection: 'none' }} openOnHover={false} openOnFocus={false} openOnClick padding="12px" {width} height="available" style="overflow: hidden auto" triggerProps={{ tag: 'span', class: 'tip' }}>
	{#snippet tooltip()}
		<div class="pop">{@render content(close)}</div>
	{/snippet}
	{@render children()}
</NeoTooltip>

<style>
	.pop {
		display: flex;
		flex-direction: column;
		gap: 12px;
		min-width: 16rem;
		max-width: 28rem;
	}
</style>
