<script lang="ts">
	import type { Snippet } from 'svelte';
	import NeoLabel from '#lib/neo/inputs/common/NeoLabel.svelte';
	import InfoTip from '#lib/ui/InfoTip.svelte';

	// The line over a control, its info behind a button after the words, and whatever trails to the right
	let { label, info, forId, required = false, end }: { label?: string; info?: string; forId?: string; required?: boolean; end?: Snippet } = $props();
</script>

{#if label}
	<span class="fieldlabel">
		<NeoLabel {label} for={forId} {required} />
		{#if info}<InfoTip text={info} about={label} />{/if}
		{#if end}<span class="fill"></span>{@render end()}{/if}
	</span>
{/if}

<style>
	.fieldlabel {
		display: flex;
		gap: 6px;
		align-items: center;
		min-width: 0;
		min-height: 24px;
	}
	.fieldlabel :global(.neo-label-container) {
		min-width: 0;
	}
	/* A step under body text, so section headings above the fields read as headings */
	.fieldlabel :global(.neo-label) {
		font-size: var(--nebu-text-secondary);
		overflow: visible;
		white-space: normal;
		text-overflow: clip;
	}
</style>
