<script lang="ts">
	import { NeoPill } from '#lib/neo/pill/index.js';
	import Bone from '#lib/ui/Bone.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import type { ContextRef, Contexts } from '#lib/pages/models/context.svelte.js';
	import WarningIcon from 'phosphor-svelte/lib/WarningIcon';

	// The memory a weight group fits in here and the largest context it reaches there, in words, planned once the row scrolls into view
	let { contexts, of }: { contexts: Contexts; of: ContextRef } = $props();

	const state = $derived(contexts.get(of));

	// Asks for the plan when the pill first shows, so a long list plans only the rows looked at
	function seen(node: HTMLElement) {
		const io = new IntersectionObserver((entries) => {
			if (!entries.some((e) => e.isIntersecting)) return;
			contexts.want(of);
			io.disconnect();
		});
		io.observe(node);
		return () => io.disconnect();
	}
</script>

<span class="fit" {@attach seen}>
	{#if !state}
		<Bone width="5rem" height="20px" />
	{:else if state.error}
		<Tip text={state.error} tag="span"><span class="failed" role="img" aria-label={state.error}><WarningIcon size={16} /><span class="word">Not planned</span></span></Tip>
	{:else if state.fit}
		<Tip text={state.fit.tip} tag="span">
			<NeoPill size="small" tinted color={state.fit.lamp} label={[state.fit.where, state.fit.context].filter(Boolean).join(' \u00b7 ')} aria-label={state.fit.tip} />
		</Tip>
	{:else}
		<span class="word muted">No runtime loads it</span>
	{/if}
</span>

<style>
	.fit {
		display: inline-flex;
		align-items: center;
		min-height: 20px;
	}
	.failed {
		display: inline-flex;
		gap: 4px;
		align-items: center;
		color: var(--neo-color-warning);
	}
	.word {
		font-size: var(--nebu-text-caption);
		white-space: nowrap;
	}
</style>
