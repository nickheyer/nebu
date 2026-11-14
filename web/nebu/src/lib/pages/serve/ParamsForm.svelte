<script lang="ts">
	import { SvelteSet } from 'svelte/reactivity';
	import type { ParamState } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { ParamList, ParamType, type Param } from '#lib/proto/nebu/v1/runtime_pb.js';
	import ParamField from '#lib/pages/serve/ParamField.svelte';
	import CaretDownIcon from 'phosphor-svelte/lib/CaretDownIcon';
	import CaretRightIcon from 'phosphor-svelte/lib/CaretRightIcon';

	// Every parameter of a runtime by group on one grid, each offered once the values it depends on call for it
	let { params, values = $bindable({}), supplied = {}, states = [] }: { params: Param[]; values?: Record<string, string>; supplied?: Record<string, string>; states?: ParamState[] } = $props();

	type Group = { title: string; plain: Param[]; advanced: Param[] };

	// Scaled lists another list's control fills, which draw no control of their own
	const folded = $derived(new Set(params.map((p) => p.scaledIn).filter(Boolean)));
	const opened = new SvelteSet<string>();
	// Advanced params seen holding a value, which stay in view while their group is closed
	const pinned = new SvelteSet<string>();

	$effect(() => {
		for (const p of params) if (p.advanced && (values[p.name] ?? '') !== '') pinned.add(p.name);
	});

	// Whether a parameter is set, or a parameter it depends on holds a value that calls for it
	function offered(p: Param): boolean {
		if (p.when.length === 0 || (values[p.name] ?? '') !== '') return true;
		return p.when.some((w) => {
			const held = values[w.param] ?? '';
			return w.set ? held !== '' : w.values.includes(held);
		});
	}

	// One group per heading in first seen order, a group offering nothing left out
	const groups = $derived.by(() => {
		const by = new Map<string, Group>();
		for (const p of params) {
			if (folded.has(p.name) || !offered(p)) continue;
			const title = p.group || 'Parameters';
			const g = by.get(title) ?? { title, plain: [], advanced: [] };
			by.set(title, g);
			(p.advanced ? g.advanced : g.plain).push(p);
		}
		return [...by.values()];
	});
	// Groups with everyday params stand open under their heading, the rest fold to one line each below them
	const standing = $derived(groups.filter((g) => g.plain.length > 0));
	const seldom = $derived(groups.filter((g) => g.plain.length === 0));

	// The params a group draws, its advanced ones once opened or set
	function shown(g: Group): Param[] {
		const open = opened.has(g.title);
		return [...g.plain, ...g.advanced.filter((p) => open || pinned.has(p.name))];
	}

	function stateOf(name: string): ParamState | undefined {
		return states.find((s) => s.name === name);
	}

	function toggle(title: string) {
		if (!opened.delete(title)) opened.add(title);
	}
</script>

{#snippet grid(list: Param[])}
	{#if list.length}
		<div class="grid">
			{#each list as p (p.name)}
				<div class="cell" class:late={p.advanced} class:full={p.list !== ParamList.UNSPECIFIED} class:wide={p.type === ParamType.PATH || p.picks !== ''}>
					<ParamField param={p} bind:values supplied={supplied[p.name]} state={stateOf(p.name)} />
				</div>
			{/each}
		</div>
	{/if}
{/snippet}

<div class="params">
	{#each standing as g (g.title)}
		<section class="group" aria-label={g.title}>
			<div class="head">
				<h2>{g.title}</h2>
				{#if g.advanced.length}
					<button type="button" class="more" aria-expanded={opened.has(g.title)} onclick={() => toggle(g.title)}>
						Advanced
						<span class="caret" class:turned={opened.has(g.title)}><CaretDownIcon size={14} /></span>
					</button>
				{/if}
			</div>
			{@render grid(shown(g))}
		</section>
	{/each}
	{#if seldom.length}
		<div class="seldom">
			{#each seldom as g (g.title)}
				<section class="group" aria-label={g.title}>
					<h2 class="seldom-head">
						<button type="button" class="fold" aria-expanded={opened.has(g.title)} onclick={() => toggle(g.title)}>
							<span class="caret" class:quarter={opened.has(g.title)}><CaretRightIcon size={16} /></span>
							{g.title}
						</button>
					</h2>
					{@render grid(shown(g))}
				</section>
			{/each}
		</div>
	{/if}
</div>

<style>
	.params {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-5);
		min-width: 0;
		container: params / inline-size;
	}
	.group {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
	}
	.head {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		justify-content: space-between;
		min-height: 28px;
	}
	h2 {
		font-size: var(--nebu-text-body);
	}
	/* As many equal columns as the form holds, so every box of every group shares its edges */
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(100%, 15rem), 1fr));
		gap: var(--nebu-space-3) var(--nebu-space-4);
		align-items: start;
		min-width: 0;
	}
	.cell {
		min-width: 0;
	}
	.late {
		animation: nebu-reveal 220ms var(--nebu-settle) backwards;
	}
	.full {
		grid-column: 1 / -1;
	}
	@container params (min-width: 30rem) {
		.wide {
			grid-column: span 2;
		}
	}
	.more,
	.fold {
		display: inline-flex;
		align-items: center;
		margin: 0;
		padding: 0;
		font: inherit;
		color: inherit;
		background: none;
		border: none;
		border-radius: var(--neo-border-radius);
		cursor: pointer;
		appearance: none;
	}
	.more {
		gap: var(--nebu-space-1);
		min-height: 28px;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		transition: color var(--nebu-fast) var(--nebu-ease);
	}
	.more:hover,
	.more[aria-expanded='true'] {
		color: var(--neo-text-color);
	}
	.more:focus-visible,
	.fold:focus-visible {
		outline: 2px solid var(--neo-color-primary);
		outline-offset: 2px;
	}
	.caret {
		display: inline-flex;
		color: var(--nebu-text-muted);
		transition: transform var(--nebu-normal) var(--nebu-settle);
	}
	.turned {
		transform: rotate(180deg);
	}
	.quarter {
		transform: rotate(90deg);
	}
	/* Groups of seldom touched params stack as one tight list, a line apiece until opened */
	.seldom {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.seldom-head {
		display: flex;
		font-weight: 400;
	}
	.fold {
		flex: 1 1 auto;
		gap: 6px;
		min-height: var(--nebu-control);
		letter-spacing: inherit;
		text-align: start;
		transition: color var(--nebu-fast) var(--nebu-ease);
	}
	.fold:hover {
		color: var(--neo-text-color-highlight);
	}
	.seldom .group:has(.grid) {
		padding-bottom: var(--nebu-space-4);
	}
</style>
