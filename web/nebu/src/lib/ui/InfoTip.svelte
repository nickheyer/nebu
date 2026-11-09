<script lang="ts">
	import Tip from '#lib/ui/Tip.svelte';
	import InfoIcon from 'phosphor-svelte/lib/InfoIcon';

	// The mark after a page or block title holding one static line
	let { text }: { text: string } = $props();
</script>

<Tip {text} tag="span">
	<span class="info" role="img" aria-label="Info"><InfoIcon size={14} /></span>
</Tip>

<style>
	.info {
		display: inline-flex;
		color: var(--nebu-text-faint);
		cursor: help;
		transition: color var(--nebu-fast) var(--nebu-ease);
	}
	.info:hover {
		color: var(--neo-color-primary);
	}
</style>
