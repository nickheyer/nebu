<script lang="ts">
	import { SvelteSet } from 'svelte/reactivity';
	import type { ParamState } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { ParamList, ParamType, type Param } from '#lib/proto/nebu/v1/runtime_pb.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import ParamField from '#lib/pages/serve/ParamField.svelte';

	// Every parameter of a runtime by group under the heading its page gives it, each offered once the values it depends on call for it
	let {
		params,
		values = $bindable({}),
		supplied = {},
		states = [],
		// The word before what an unset parameter rests on
		restingWord = 'Default'
	}: { params: Param[]; values?: Record<string, string>; supplied?: Record<string, string>; states?: ParamState[]; restingWord?: string } = $props();

	type Group = { title: string; plain: Param[]; advanced: Param[] };
	type Cell = { param: Param; units: number };

	// Pixels a control needs across, and the gap between two
	const cellMin = 240;
	const gap = 16;
	let width = $state(0);
	const columns = $derived(Math.max(1, Math.min(4, Math.floor((width + gap) / (cellMin + gap)))));

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
		if (!p.when.length || (values[p.name] ?? '') !== '') return true;
		return p.when.some((w) => {
			const v = values[w.param] ?? '';
			return w.values.length ? w.values.includes(v) : v !== '';
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

	// The params a closed group keeps in view, its everyday ones and the advanced ones holding a value
	function standing(g: Group): Param[] {
		return [...g.plain, ...g.advanced.filter((p) => pinned.has(p.name))];
	}

	// Rows that each fill the form's width, a list of files alone on one and the rest spread evenly over as few as hold them
	function rows(list: Param[]): Cell[][] {
		const out: Cell[][] = [];
		let run: Cell[] = [];
		const flush = () => {
			const total = run.reduce((n, c) => n + c.units, 0);
			const cap = total === 0 ? 0 : Math.ceil(total / Math.ceil(total / columns));
			let row: Cell[] = [];
			let used = 0;
			for (const c of run) {
				if (row.length && used + c.units > cap) {
					out.push(row);
					row = [];
					used = 0;
				}
				row.push(c);
				used += c.units;
			}
			if (row.length) out.push(row);
			run = [];
		};
		for (const param of list) {
			if (param.list !== ParamList.UNSPECIFIED) {
				flush();
				out.push([{ param, units: columns }]);
			} else {
				run.push({ param, units: Math.min(columns, param.type === ParamType.PATH || param.picks !== '' ? 2 : 1) });
			}
		}
		flush();
		return out;
	}

	function stateOf(name: string): ParamState | undefined {
		return states.find((s) => s.name === name);
	}

	function fold(title: string, open: boolean) {
		if (open) opened.add(title);
		else opened.delete(title);
	}
</script>

{#snippet fields(list: Param[])}
	{#if list.length}
		<div class="rows">
			{#each rows(list) as row (row[0]!.param.name)}
				<div class="row">
					{#each row as c (c.param.name)}
						<div class="cell" style:--units={c.units}>
							<ParamField param={c.param} bind:values supplied={supplied[c.param.name]} state={stateOf(c.param.name)} {restingWord} />
						</div>
					{/each}
				</div>
			{/each}
		</div>
	{/if}
{/snippet}

<!-- A group with everyday params shows them under its title and folds the rest, one of advanced params alone folds whole under its title -->
<div class="params" bind:clientWidth={width}>
	{#each groups as g (g.title)}
		{@const open = opened.has(g.title)}
		<section class="group" aria-label={g.title}>
			{#if g.plain.length}
				<h3 class="title">{g.title}</h3>
				{@render fields(open ? g.plain : standing(g))}
				{#if g.advanced.length}
					<Collapse bind:open={() => open, (v) => fold(g.title, v)} label="Advanced {g.title.toLowerCase()}" quiet indent={false}>
						<div class="folded">{@render fields(g.advanced)}</div>
					</Collapse>
				{/if}
			{:else}
				<Collapse bind:open={() => open, (v) => fold(g.title, v)} label={g.title} group indent={false}>
					<div class="folded">{@render fields(g.advanced)}</div>
				</Collapse>
				{#if !open}{@render fields(standing(g))}{/if}
			{/if}
		</section>
	{/each}
</div>

<style>
	.params {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-4);
		min-width: 0;
	}
	.group {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
	}
	/* A group's title sits a step under the section title of the page that holds the form */
	.title {
		font-size: var(--nebu-text-secondary);
		font-weight: 500;
		line-height: 24px;
		letter-spacing: 0;
		color: var(--nebu-text-muted);
	}
	.rows {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
	}
	/* Every row spans the form, its cells sharing it by the columns each takes */
	.row {
		display: flex;
		gap: var(--nebu-space-4);
		align-items: flex-start;
		min-width: 0;
	}
	.cell {
		flex: var(--units) 1 0;
		min-width: 0;
	}
	/* What a fold opens hangs from a rule under its trigger, apart from the fields that stand open */
	.folded {
		margin-left: 7px;
		padding: var(--nebu-space-1) 0 var(--nebu-space-1) var(--nebu-space-3);
		border-left: 2px solid var(--nebu-border);
		animation: nebu-reveal 220ms var(--nebu-settle) backwards;
	}
</style>
