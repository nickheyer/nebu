<script lang="ts">
	import { ParamList, type Param } from '#lib/proto/nebu/v1/runtime_pb.js';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import ParamField from '#lib/pages/serve/ParamField.svelte';

	// Every parameter of a runtime by group, each offered once the values it depends on call for it
	let { params, values = $bindable({}), supplied = {} }: { params: Param[]; values?: Record<string, string>; supplied?: Record<string, string> } = $props();

	// Scaled lists another list's control fills, which draw no control of their own
	const folded = $derived(new Set(params.map((p) => p.scaledIn).filter(Boolean)));

	// Whether a parameter is set, or a parameter it depends on holds a value that calls for it
	function offered(p: Param): boolean {
		if (p.when.length === 0 || (values[p.name] ?? '') !== '') return true;
		return p.when.some((w) => {
			const held = values[w.param] ?? '';
			return w.set ? held !== '' : w.values.includes(held);
		});
	}

	// One section per group in first seen order, advanced params under each section's collapse, a group offering nothing left out
	const groups = $derived.by(() => {
		const order: string[] = [];
		const by = new Map<string, { plain: Param[]; advanced: Param[] }>();
		for (const p of params) {
			if (folded.has(p.name) || !offered(p)) continue;
			const g = p.group || 'Parameters';
			if (!by.has(g)) {
				by.set(g, { plain: [], advanced: [] });
				order.push(g);
			}
			(p.advanced ? by.get(g)!.advanced : by.get(g)!.plain).push(p);
		}
		return order.map((g) => ({ title: g, ...by.get(g)! }));
	});

	// Two fields to a row, a list of files taking a row of its own
	function rows(list: Param[]): Param[][] {
		const out: Param[][] = [];
		let open: Param[] | undefined;
		for (const p of list) {
			if (p.list !== ParamList.UNSPECIFIED) {
				out.push([p]);
				open = undefined;
			} else if (open) {
				open.push(p);
				open = undefined;
			} else {
				open = [p];
				out.push(open);
			}
		}
		return out;
	}
</script>

{#snippet fields(list: Param[])}
	{#each rows(list) as row (row[0]!.name)}
		<FieldRow>
			{#each row as p (p.name)}
				<ParamField param={p} bind:values supplied={supplied[p.name]} />
			{/each}
		</FieldRow>
	{/each}
{/snippet}

<!-- A group of only advanced params folds whole under its title, one with none shows no disclosure -->
{#each groups as g (g.title)}
	{#if g.plain.length === 0}
		<FormSection title={g.title} collapsible>
			{@render fields(g.advanced)}
		</FormSection>
	{:else}
		{#snippet more()}{@render fields(g.advanced)}{/snippet}
		<FormSection title={g.title} advanced={g.advanced.length ? more : undefined}>
			{@render fields(g.plain)}
		</FormSection>
	{/if}
{/each}
