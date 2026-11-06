<script lang="ts">
	import type { Snippet } from 'svelte';
	import Tip, { type Fact } from '#lib/ui/Tip.svelte';
	import InfoIcon from 'phosphor-svelte/lib/InfoIcon';

	let { text, facts, click = false, content }: { text?: string; facts?: Fact[]; click?: boolean; content?: Snippet } = $props();
</script>

<Tip {text} {facts} {click} {content} tag="span">
	<span class="info" role="img" aria-label="Info"><InfoIcon size={16} /></span>
</Tip>

<style>
	.info {
		display: inline-flex;
		color: var(--nebu-text-muted);
		cursor: help;
	}
</style>
