<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { FacetInput, type Facet } from '#lib/proto/nebu/v1/source_pb.js';
	import Count from '#lib/ui/Count.svelte';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import Options from '#lib/ui/fields/Options.svelte';
	import Range from '#lib/ui/fields/Range.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import { facetText } from '#lib/pages/discover/discover.js';
	import CaretLeftIcon from 'phosphor-svelte/lib/CaretLeftIcon';
	import CaretRightIcon from 'phosphor-svelte/lib/CaretRightIcon';

	// A source's filters: their names down one side, and beside them what the one in view takes.
	// Narrow, the names fill the panel and the one picked opens over them.
	let {
		facets,
		filters,
		active = $bindable(''),
		stacked = false,
		onset
	}: {
		facets: Facet[];
		// The values set, by facet id
		filters: Record<string, string[]>;
		// The id of the facet in view
		active?: string;
		stacked?: boolean;
		onset: (f: Facet, values: string[]) => void;
	} = $props();

	// A toggle switches in its row, every other facet opens beside the names
	const opens = (f: Facet) => f.input !== FacetInput.TOGGLE;
	const facet = $derived(facets.find((f) => f.id === active && opens(f)) ?? (stacked ? undefined : facets.find(opens)));
	const held = $derived(facet ? (filters[facet.id] ?? []) : []);
	const options = $derived((facet?.values ?? []).map((v) => ({ value: v.id, label: v.label || v.id, group: v.group || undefined })));
</script>

<div class="panel" class:stacked>
	{#if !stacked || !facet}
		<div class="names" role="group" aria-label="Filters">
			{#each facets as f (f.id)}
				{@const set = filters[f.id] ?? []}
				{#if opens(f)}
					<button type="button" class="name" class:current={f === facet} aria-current={f === facet ? 'true' : undefined} onclick={() => (active = f.id)}>
						<span class="facet">{f.label}</span>
						{#if stacked}
							<span class="set">{facetText(f, set)}</span>
							<CaretRightIcon size={14} />
						{:else if set.length}
							<Count n={set.length} />
						{/if}
					</button>
				{:else}
					<label class="name">
						<span class="facet">{f.label}</span>
						<Switch checked={set[0] === 'true'} aria={f.label} onchange={(on) => onset(f, on ? ['true'] : [])} />
					</label>
				{/if}
			{/each}
		</div>
	{/if}
	{#if facet}
		<section class="values" aria-label={facet.label}>
			<header class="top">
				{#if stacked}
					<NeoButton text aria-label="All filters" onclick={() => (active = '')}>
						{#snippet icon()}<CaretLeftIcon size={16} />{/snippet}
					</NeoButton>
				{/if}
				<h3>{facet.label}</h3>
				<span class="fill"></span>
				{#if held.length}<NeoButton text onclick={() => onset(facet, [])}>Clear</NeoButton>{/if}
			</header>
			{#key facet.id}
				{#if facet.input === FacetInput.RANGE}
					<Range value={held[0] ?? ''} spans={options} onchange={(v) => onset(facet, v ? [v] : [])} />
				{:else if facet.input === FacetInput.TEXT && facet.multi}
					<Chips value={held} aria={facet.label} placeholder="Type one and press Enter" onchange={(v) => onset(facet, v)} />
				{:else if facet.input === FacetInput.TEXT}
					<Text value={held[0] ?? ''} aria={facet.label} placeholder="Type and press Enter" max={128} onchange={(v) => onset(facet, v.trim() ? [v.trim()] : [])} />
				{:else}
					<Options value={held} {options} label={facet.label} onchange={(v) => onset(facet, v)} />
				{/if}
			{/key}
		</section>
	{/if}
</div>

<style>
	/* The names set the panel's height, as tall as the window leaves room for, and the values scroll inside it */
	.panel {
		display: grid;
		grid-template-rows: minmax(20rem, auto);
		grid-template-columns: 15rem minmax(0, 1fr);
		width: min(40rem, calc(100vw - 2rem));
		max-height: calc(100dvh - 14rem);
	}
	.panel.stacked {
		grid-template-rows: minmax(0, 1fr);
		grid-template-columns: minmax(0, 1fr);
		width: auto;
		height: min(32rem, calc(100dvh - 18rem));
		max-height: none;
	}
	.names {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-height: 0;
		padding: var(--nebu-space-2);
		overflow-y: auto;
		overscroll-behavior: contain;
		border-right: 1px solid var(--nebu-border);
	}
	.stacked .names {
		padding: 0;
		border-right: 0;
	}
	.name {
		display: flex;
		flex: 0 0 auto;
		gap: var(--nebu-space-2);
		align-items: center;
		width: 100%;
		min-width: 0;
		min-height: var(--nebu-row-dense);
		padding: 0 var(--nebu-space-3);
		font: inherit;
		color: inherit;
		text-align: left;
		cursor: pointer;
		background: none;
		border: 0;
		border-radius: 6px;
		appearance: none;
	}
	.name:hover {
		background: var(--neo-background-color-hover);
	}
	.name:focus-visible {
		outline: 2px solid var(--neo-color-primary);
		outline-offset: -2px;
	}
	/* The facet in view is pressed into the panel, as the page in view is on the rail */
	.name.current {
		color: var(--neo-color-primary);
		box-shadow: var(--neo-box-shadow-pressed-2);
	}
	.facet {
		flex: 1 1 auto;
		min-width: 0;
		overflow: hidden;
		white-space: nowrap;
		text-overflow: ellipsis;
	}
	/* Narrow, a row says what its facet is set to */
	.set {
		flex: 0 1 auto;
		min-width: 0;
		overflow: hidden;
		font-size: var(--nebu-text-secondary);
		color: var(--neo-color-primary);
		white-space: nowrap;
		text-overflow: ellipsis;
	}
	.values {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-3);
		min-width: 0;
		min-height: 0;
		padding: var(--nebu-space-3) var(--nebu-space-4) var(--nebu-space-4);
		contain: size;
	}
	.stacked .values {
		padding: 0;
		contain: none;
	}
	.top {
		display: flex;
		flex: 0 0 auto;
		gap: var(--nebu-space-2);
		align-items: center;
		min-height: var(--nebu-control);
	}
</style>
