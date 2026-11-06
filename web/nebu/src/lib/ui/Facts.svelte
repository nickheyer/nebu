<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoProgressBar } from '#lib/neo/progress/index.js';
	import Dot from '#lib/ui/Dot.svelte';
	import Sparkline from '#lib/ui/Sparkline.svelte';
	import Tip, { type Fact } from '#lib/ui/Tip.svelte';
	import type { Lamp } from '#lib/ui/status.js';

	export type FactItem = {
		id: string;
		label: string;
		value: string;
		href?: string;
		facts?: Fact[];
		tip?: string;
		lamp?: Lamp;
		// A small instrument beside the value, a sparkline, a row of lamps, or a meter filled to a fraction
		spark?: number[];
		dots?: Lamp[];
		fraction?: number;
	};

	// A flat strip of live facts sharing a row equally, each a label and value with a tooltip
	let { items, size = 'page', after }: { items: FactItem[]; size?: 'page' | 'section'; after?: Snippet } = $props();
</script>

<div class="facts" class:section={size === 'section'} role="list">
	{#each items as it (it.id)}
		<Tip facts={it.facts} text={it.tip} tag="div" class="factcell">
			<svelte:element this={it.href ? 'a' : 'div'} href={it.href} class="fact" role="listitem">
				<span class="label">{it.label}</span>
				<span class="value" class:warning-text={it.lamp === 'warning'} class:error-text={it.lamp === 'error'} class:success-text={it.lamp === 'success'}>{it.value}</span>
				{#if it.spark?.some((v) => v > 0)}
					<Sparkline points={it.spark} width={64} height={16} />
				{:else if it.dots?.length}
					<span class="dots">
						{#each it.dots.slice(0, 12) as d, i (i)}<Dot lamp={d} size={6} />{/each}
						{#if it.dots.length > 12}<span class="more">+{it.dots.length - 12}</span>{/if}
					</span>
				{:else if it.fraction !== undefined}
					<span class="meter">
						<NeoProgressBar value={Math.min(100, it.fraction * 100)} elevation={-1} height="6px" width="48px" color={it.fraction > 1 ? 'warning' : 'primary'} immediate />
					</span>
				{/if}
			</svelte:element>
		</Tip>
	{/each}
	{#if after}<div class="after">{@render after()}</div>{/if}
</div>

<style>
	.facts {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 0;
		min-width: 0;
		padding: 4px 0;
		border-bottom: 1px solid var(--nebu-border);
	}
	.facts :global(.factcell) {
		display: flex;
		flex: 1 1 11rem;
		min-width: 11rem;
	}
	.fact {
		display: flex;
		flex: 1 1 auto;
		gap: 8px;
		align-items: center;
		min-width: 0;
		min-height: 36px;
		padding: 0 12px;
		color: inherit;
		text-decoration: none;
		border-radius: 8px;
		transition: background 150ms ease-out;
	}
	a.fact:hover {
		background: var(--neo-background-color-hover);
	}
	.label {
		font-size: 13px;
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
	.value {
		font-weight: 500;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
	.after {
		display: flex;
		flex: 0 0 auto;
		align-items: center;
		padding: 0 12px;
	}
	.dots {
		display: inline-flex;
		gap: 3px;
		align-items: center;
	}
	.more {
		font-size: 12px;
		color: var(--nebu-text-muted);
	}
	.meter {
		display: inline-flex;
		flex: 0 0 auto;
		align-items: center;
	}
	.section .fact {
		min-height: 28px;
	}
	.section .value {
		font-size: 13px;
	}
	@media (prefers-reduced-motion: reduce) {
		.fact {
			transition: none;
		}
	}
</style>
