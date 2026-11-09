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
		mono?: boolean;
		spark?: number[];
		dots?: Lamp[];
		fraction?: number;
	};

	// Live readings sharing a row, each a label over a value with a tooltip
	let { items, size = 'page', after }: { items: FactItem[]; size?: 'page' | 'section'; after?: Snippet } = $props();
</script>

<div class="facts" class:section={size === 'section'} role="list">
	{#each items as it (it.id)}
		<div class="cell">
			<Tip facts={it.facts} text={it.tip} tag="div" class="factcell">
			<svelte:element this={it.href ? 'a' : 'div'} href={it.href} class="fact" role="listitem">
				<span class="label">{it.label}</span>
				<span class="value" class:mono={it.mono} class:warning-text={it.lamp === 'warning'} class:error-text={it.lamp === 'error'} class:success-text={it.lamp === 'success'}>{it.value}</span>
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
		</div>
	{/each}
	{#if after}<div class="after">{@render after()}</div>{/if}
</div>

<style>
	.facts {
		display: flex;
		flex: 1 1 100%;
		flex-wrap: wrap;
		gap: var(--nebu-space-3) 0;
		width: 100%;
		min-width: 0;
		container-type: inline-size;
	}
	.cell {
		display: flex;
		flex: 1 1 11rem;
		min-width: 11rem;
		padding: 0 var(--nebu-space-4);
		border-left: 1px solid var(--nebu-border);
	}
	.cell:first-child {
		padding-left: 0;
		border-left: 0;
	}
	.cell > :global(.factcell) {
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
	}
	@container (max-width: 47.99rem) {
		.cell {
			flex: 1 1 calc(50% - 8px);
			min-width: 0;
		}
	}
	.section .cell {
		flex: 0 0 auto;
		min-width: 0;
	}
	/* Rows pack at the top so every label sits on one line across the cells whatever its value's size */
	.fact {
		display: grid;
		flex: 1 1 auto;
		grid-template-columns: minmax(0, 1fr) auto;
		column-gap: var(--nebu-space-3);
		align-content: start;
		align-items: end;
		min-width: 0;
		color: inherit;
		text-decoration: none;
		border-radius: var(--neo-border-radius);
		transition: background var(--nebu-fast) var(--nebu-ease);
	}
	a.fact:hover .value {
		color: var(--neo-color-primary);
	}
	.label {
		grid-column: 1 / -1;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
	.value {
		min-width: 0;
		overflow: hidden;
		font-size: 22px;
		font-weight: 300;
		font-variant-numeric: tabular-nums;
		line-height: 28px;
		letter-spacing: -0.01em;
		white-space: nowrap;
		text-overflow: ellipsis;
		animation: nebu-reveal 300ms var(--nebu-settle) backwards;
	}
	/* Mono values share the line box of the large figures so their baselines meet */
	.value.mono {
		font-family: var(--nebu-font-mono);
		font-size: 17px;
		font-weight: 300;
	}
	.after {
		display: flex;
		flex: 0 0 auto;
		align-items: center;
		padding: 0 var(--nebu-space-3);
	}
	.dots {
		display: inline-flex;
		gap: 3px;
		align-items: center;
	}
	.more {
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-muted);
	}
	.meter {
		display: inline-flex;
		flex: 0 0 auto;
		align-items: center;
	}
	.section .value {
		font-size: var(--nebu-text-body);
	}
</style>
