<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoCollapse } from '#lib/neo/collapse/index.js';
	import CaretRightIcon from 'phosphor-svelte/lib/CaretRightIcon';

	// A short section under one trigger, a section title, a group title, quiet words or plain ones, its body set in on both sides unless told to line up with the caret
	let { label, open = $bindable(false), heading = false, group = false, quiet = false, indent = true, children }: { label: string; open?: boolean; heading?: boolean; group?: boolean; quiet?: boolean; indent?: boolean; children: Snippet } = $props();
</script>

{#snippet trigger()}
	<span class="trigger" class:heading class:group class:quiet>
		<span class="caret" class:open><CaretRightIcon size={16} /></span>
		<span class="text">{label}</span>
	</span>
{/snippet}

<NeoCollapse bind:open label={trigger}>
	<div class="body" class:indent>{@render children()}</div>
</NeoCollapse>

<style>
	.trigger {
		display: inline-flex;
		gap: 6px;
		align-items: center;
	}
	/* A section title doubling as the trigger keeps the weight of the headings beside it */
	.trigger.heading .text {
		font-size: var(--nebu-text-body);
		font-weight: 500;
		letter-spacing: -0.01em;
	}
	/* A group title doubling as the trigger reads as the group titles that stand open beside it */
	.trigger.group .text {
		font-size: var(--nebu-text-secondary);
		font-weight: 500;
		color: var(--nebu-text-muted);
	}
	/* A trigger for what a group holds back reads a step under the group's own title */
	.trigger.quiet .text {
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
	}
	.caret {
		display: inline-flex;
		color: var(--nebu-text-muted);
		transition: transform var(--nebu-normal) var(--nebu-settle);
	}
	.caret.open {
		transform: rotate(90deg);
	}
	.body {
		padding: 4px 0 8px;
	}
	.body.indent {
		padding-inline: 22px;
	}
</style>
