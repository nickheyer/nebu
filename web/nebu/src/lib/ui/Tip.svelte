<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoTooltip } from '#lib/neo/floating/tooltips/index.js';
	import type { NeoTooltipPlacement } from '#lib/neo/floating/common/neo-placement.model.js';
	import { capitalize } from '#lib/format.js';

	export type Fact = { label: string; value: string };

	let {
		text,
		facts,
		placement = 'top',
		tag = 'span',
		click = false,
		disabled = false,
		content,
		children,
		class: cls = ''
	}: {
		text?: string;
		facts?: Fact[];
		placement?: NeoTooltipPlacement;
		tag?: 'span' | 'div';
		click?: boolean;
		disabled?: boolean;
		content?: Snippet;
		children: Snippet;
		class?: string;
	} = $props();

	const empty = $derived(!text && !facts?.length && !content);
</script>

{#if empty || disabled}
	{@render children()}
{:else}
	<NeoTooltip
		{placement}
		elevation={2}
		filled
		portal
		flipOptions={{ fallbackAxisSideDirection: 'none' }}
		openDelay={100}
		hoverDelay={click ? 0 : 300}
		openOnHover={!click}
		openOnFocus={!click}
		openOnClick={click}
		padding="8px 12px"
		triggerProps={{ tag, class: `tip ${cls}` }}
	>
		{#snippet tooltip()}
			{#if content}
				{@render content()}
			{:else if facts}
				<div class="facts">
					{#each facts as f (f.label)}
						<div class="fact">
							<span class="k">{capitalize(f.label)}</span>
							<span class="v">{f.value}</span>
						</div>
					{/each}
				</div>
			{:else}
				<span class="line">{text}</span>
			{/if}
		{/snippet}
		{@render children()}
	</NeoTooltip>
{/if}

<style>
	:global(.tip) {
		display: inline-flex;
		min-width: 0;
		max-width: 100%;
	}
	.line {
		display: block;
		max-width: 40ch;
		font-size: 13px;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.facts {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 16ch;
		max-width: 48ch;
		font-size: 13px;
	}
	.fact {
		display: flex;
		gap: 12px;
		justify-content: space-between;
	}
	.k {
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
	.v {
		font-variant-numeric: tabular-nums;
		text-align: right;
		overflow-wrap: anywhere;
	}
</style>
