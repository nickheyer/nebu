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
		// A line under the value saying what it means or how it was measured
		note?: string;
		// A value that states no reading, such as a limit left unset
		quiet?: boolean;
		// A long value such as an address, which takes two cells
		wide?: boolean;
		// Characters ending the value that stay whole when it clips, such as a port
		keep?: number;
	};

	// Live readings sharing a row, each a label over a value with a tooltip, one without a value left out
	let { items, size = 'page', after }: { items: FactItem[]; size?: 'page' | 'section'; after?: Snippet } = $props();

	const shown = $derived(items.filter((it) => it.value !== ''));
	// Pixels a cell needs across
	const cellMin = 176;
	let width = $state(0);

	// Columns a page strip lays out, as many as fit with its rows evened so none ends on one lone cell
	const columns = $derived.by(() => {
		const slots = shown.reduce((n, it) => n + (it.wide ? 2 : 1), 0);
		if (size === 'section' || slots === 0) return 0;
		const fit = width === 0 ? slots : width < 768 ? 2 : Math.max(2, Math.floor(width / cellMin));
		const rows = Math.ceil(slots / Math.min(fit, slots));
		return Math.ceil(slots / rows);
	});
	// Where each cell sits, its span and whether it starts a row
	const placed = $derived.by(() => {
		let at = 0;
		return shown.map((it) => {
			const span = columns === 0 ? 1 : Math.min(it.wide ? 2 : 1, columns);
			if (columns > 0 && at + span > columns) at = 0;
			const first = at === 0;
			at = columns === 0 ? at + span : (at + span) % columns;
			return { it, span, first };
		});
	});

	// A figure and the unit after it, the whole value as the figure when it carries no unit
	function parts(it: FactItem): { figure: string; unit: string } {
		const m = it.mono ? null : /^([+-]?[\d.,]+)[\s  ]+([^\d\s][^\d]*)$/.exec(it.value);
		return m ? { figure: m[1]!, unit: m[2]! } : { figure: it.value, unit: '' };
	}
</script>

<div class="facts" class:section={size === 'section'} role="list" style:grid-template-columns={columns ? `repeat(${columns}, minmax(0, 1fr))` : undefined} bind:clientWidth={width}>
	{#each placed as { it, span, first } (it.id)}
		{@const p = parts(it)}
		<div class="cell" class:first style:grid-column={columns ? `span ${span}` : undefined}>
			<Tip facts={it.facts} text={it.tip === it.value ? undefined : it.tip} tag="div" class="factcell">
			<svelte:element this={it.href ? 'a' : 'div'} href={it.href} class="fact" role="listitem">
				<span class="label">{it.label}</span>
				{#if it.keep}
					<span class="value kept" class:mono={it.mono} class:warning-text={it.lamp === 'warning'} class:error-text={it.lamp === 'error'} class:success-text={it.lamp === 'success'}><span class="lead">{it.value.slice(0, -it.keep)}</span><span class="tail">{it.value.slice(-it.keep)}</span></span>
				{:else}
					<span class="value" class:quiet={it.quiet} class:mono={it.mono} class:warning-text={it.lamp === 'warning'} class:error-text={it.lamp === 'error'} class:success-text={it.lamp === 'success'}>{p.figure}{#if p.unit}<span class="unit">{p.unit}</span>{/if}</span>
				{/if}
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
				{#if it.note}<span class="note">{it.note}</span>{/if}
			</svelte:element>
			</Tip>
		</div>
	{/each}
	{#if after}<div class="after">{@render after()}</div>{/if}
</div>

<style>
	/* A page strip is a grid of equal columns, every row starting on the strip's own edge */
	.facts {
		display: grid;
		flex: 1 1 100%;
		gap: var(--nebu-space-3) 0;
		width: 100%;
		min-width: 0;
	}
	.cell {
		display: flex;
		min-width: 0;
		padding: 0 var(--nebu-space-4);
		border-left: 1px solid var(--nebu-border);
	}
	.cell.first {
		padding-left: 0;
		border-left: 0;
	}
	.cell > :global(.factcell) {
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
	}
	.facts.section {
		display: flex;
		flex-wrap: wrap;
	}
	.section .cell {
		flex: 0 0 auto;
	}
	.section .cell:first-child {
		padding-left: 0;
		border-left: 0;
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
	.value.quiet {
		color: var(--nebu-text-muted);
	}
	.note {
		grid-column: 1 / -1;
		font-size: var(--nebu-text-caption);
		line-height: 16px;
		color: var(--nebu-text-muted);
	}
	/* A value that keeps its end whole clips before it */
	.value.kept {
		display: flex;
		overflow: visible;
	}
	.lead {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.tail {
		flex: 0 0 auto;
	}
	.unit {
		margin-left: 4px;
		font-size: var(--nebu-text-secondary);
		font-weight: 400;
		letter-spacing: 0;
		color: var(--nebu-text-muted);
	}
	/* Mono values keep the size of the figures beside them */
	.value.mono {
		font-family: var(--nebu-font-mono);
		font-size: 22px;
		font-weight: 300;
	}
	.section .value.mono {
		font-size: var(--nebu-text-body);
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
