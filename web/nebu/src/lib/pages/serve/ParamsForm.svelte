<script lang="ts">
	import type { ParamState } from '#lib/proto/nebu/v1/estimate_pb.js';
	import type { Param } from '#lib/proto/nebu/v1/runtime_pb.js';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import ParamField from '#lib/pages/serve/ParamField.svelte';

	let { params, values = $bindable({}), states = [] }: { params: Param[]; values?: Record<string, string>; states?: ParamState[] } = $props();

	// One section per group in first seen order, advanced params under each section's collapse
	const groups = $derived.by(() => {
		const order: string[] = [];
		const by = new Map<string, { plain: Param[]; advanced: Param[] }>();
		for (const p of params) {
			const g = p.group || 'Parameters';
			if (!by.has(g)) {
				by.set(g, { plain: [], advanced: [] });
				order.push(g);
			}
			(p.advanced ? by.get(g)!.advanced : by.get(g)!.plain).push(p);
		}
		return order.map((g) => ({ title: g, ...by.get(g)! }));
	});

	function stateOf(name: string): ParamState | undefined {
		return states.find((s) => s.name === name);
	}

	function pairs(list: Param[]): Param[][] {
		const out: Param[][] = [];
		for (let i = 0; i < list.length; i += 2) out.push(list.slice(i, i + 2));
		return out;
	}
</script>

{#snippet fields(list: Param[])}
	{#each pairs(list) as pair, i (i)}
		<FieldRow>
			{#each pair as p (p.name)}
				<ParamField param={p} bind:values state={stateOf(p.name)} />
			{/each}
		</FieldRow>
	{/each}
{/snippet}

<!-- A group of only advanced params folds whole under its title instead of standing empty over a disclosure -->
{#each groups as g (g.title)}
	{#if g.plain.length === 0 && g.advanced.length}
		<FormSection title={g.title} collapsible>
			{@render fields(g.advanced)}
		</FormSection>
	{:else if g.plain.length}
		<FormSection title={g.title}>
			{@render fields(g.plain)}
			{#snippet advanced()}
				{#if g.advanced.length}{@render fields(g.advanced)}{/if}
			{/snippet}
		</FormSection>
	{/if}
{/each}
