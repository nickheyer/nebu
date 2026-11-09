<script lang="ts">
	import type { Snippet } from 'svelte';
	import NeoLabel from '#lib/neo/inputs/common/NeoLabel.svelte';
	import Tip from '#lib/ui/Tip.svelte';

	// The line over a control, its words carrying the info line as a tooltip, and whatever trails to the right
	let { label, info, forId, required = false, end }: { label?: string; info?: string; forId?: string; required?: boolean; end?: Snippet } = $props();
</script>

{#if label}
	<span class="fieldlabel" class:informed={info !== undefined}>
		<Tip text={info} tag="span"><NeoLabel {label} for={forId} {required} /></Tip>
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
	/* A label holding an info line wears the dotted rule of a term with a definition */
	.informed :global(.neo-label) {
		text-decoration: underline dotted var(--nebu-text-faint);
		text-decoration-thickness: 1px;
		text-underline-offset: 3px;
		cursor: help;
	}
</style>
