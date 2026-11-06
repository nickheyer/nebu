<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import CheckIcon from 'phosphor-svelte/lib/CheckIcon';
	import CopyIcon from 'phosphor-svelte/lib/CopyIcon';

	let { text, label = 'Copy' }: { text: string; label?: string } = $props();
	let done = $state(false);

	async function copy(e: Event) {
		e.stopPropagation();
		try {
			await navigator.clipboard.writeText(text);
			done = true;
			setTimeout(() => (done = false), 1500);
		} catch (err) {
			toast.error('Copy failed', err instanceof Error ? err.message : String(err));
		}
	}
</script>

<NeoButton text aria-label={label} title={label} onclick={copy} class="copy">
	{#snippet icon()}
		{#if done}<CheckIcon size={16} />{:else}<CopyIcon size={16} />{/if}
	{/snippet}
</NeoButton>

<style>
	:global(.copy) {
		--neo-btn-padding-empty: 2px;
		--neo-btn-margin: 0;
	}
</style>
