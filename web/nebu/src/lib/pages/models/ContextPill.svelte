<script lang="ts">
	import { NeoPill } from '#lib/neo/pill/index.js';
	import Bone from '#lib/ui/Bone.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import type { ContextRef, Contexts } from '#lib/pages/models/context.svelte.js';
	import WarningIcon from 'phosphor-svelte/lib/WarningIcon';

	// The largest context one weight group reaches here, coloured by the memory it takes, nothing when no runtime loads it
	let { contexts, of }: { contexts: Contexts; of: ContextRef } = $props();

	$effect(() => contexts.want(of));

	const state = $derived(contexts.get(of));
</script>

{#if !state}
	<Bone width="3.5rem" height="20px" />
{:else if state.error}
	<Tip text={state.error} tag="span"><span class="failed" role="img" aria-label={state.error}><WarningIcon size={16} /></span></Tip>
{:else if state.fit}
	<Tip text={state.fit.tip} tag="span">
		<NeoPill size="small" tinted color={state.fit.lamp} label={state.fit.label} aria-label={state.fit.tip} />
	</Tip>
{/if}

<style>
	.failed {
		display: inline-flex;
		color: var(--neo-color-warning);
	}
</style>
