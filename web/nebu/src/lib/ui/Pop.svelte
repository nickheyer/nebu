<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoTooltip } from '#lib/neo/floating/tooltips/index.js';
	import type { NeoTooltipPlacement } from '#lib/neo/floating/common/neo-placement.model.js';

	let {
		open = $bindable(false),
		placement = 'bottom-start',
		width,
		bare = false,
		content,
		children
	}: { open?: boolean; placement?: NeoTooltipPlacement; width?: string; bare?: boolean; content: Snippet<[() => void]>; children: Snippet } = $props();

	let body = $state<HTMLElement>();

	function close() {
		open = false;
	}

	$effect.pre(() => {
		if (!open && document.activeElement instanceof HTMLElement && body?.contains(document.activeElement)) document.activeElement.blur();
	});
</script>

<NeoTooltip bind:open {placement} elevation={2} filled portal flipOptions={{ fallbackAxisSideDirection: 'none' }} openOnHover={false} openOnFocus={false} openOnClick padding={bare ? '0' : '12px'} {width} height="available" style="overflow: hidden auto" triggerProps={{ tag: 'span', class: 'tip' }}>
	{#snippet tooltip()}
		<div class="pop" class:bare bind:this={body}>{@render content(close)}</div>
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
	.pop.bare {
		min-width: 0;
		max-width: none;
	}
</style>
