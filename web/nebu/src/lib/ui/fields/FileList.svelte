<script lang="ts">
	import { untrack } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import XIcon from 'phosphor-svelte/lib/XIcon';

	export type FileListShape = 'files' | 'scaled' | 'named';
	type Row = { file: string; scale: number | undefined; name: string };

	// Files a parameter lists, one row each, with the scale or the name that rides with a file
	let {
		value = $bindable(''),
		// The scaled pairs of a second parameter, whose rows join this list and take every file scaled away from one
		scaled = $bindable(undefined),
		shape = 'files',
		label,
		info,
		// What one row holds, naming the button that adds it
		noun = 'file',
		disabled = false,
		onchange
	}: {
		value?: string;
		scaled?: string;
		shape?: FileListShape;
		label?: string;
		info?: string;
		noun?: string;
		disabled?: boolean;
		onchange?: (v: string, scaled: string) => void;
	} = $props();

	const paired = $derived(scaled !== undefined);
	const scales = $derived(shape === 'scaled' || paired);
	let rows = $state<Row[]>(untrack(() => [...parse(value, shape), ...parse(scaled ?? '', 'scaled')]));

	// One row per comma separated item, the scale after its last colon and the name before its first equals sign
	function parse(text: string, as: FileListShape): Row[] {
		return text
			.split(',')
			.map((item) => item.trim())
			.filter(Boolean)
			.map((item) => {
				if (as === 'scaled') {
					const at = item.lastIndexOf(':');
					const scale = at > 0 ? Number(item.slice(at + 1)) : NaN;
					return Number.isFinite(scale) ? { file: item.slice(0, at), scale, name: '' } : { file: item, scale: 1, name: '' };
				}
				if (as === 'named') {
					const at = item.indexOf('=');
					return at > 0 ? { file: item.slice(at + 1), scale: 1, name: item.slice(0, at) } : { file: item, scale: 1, name: '' };
				}
				return { file: item, scale: 1, name: '' };
			});
	}

	// Whether a row of a paired list belongs to the scaled parameter
	function apart(r: Row): boolean {
		return paired && (r.scale ?? 1) !== 1;
	}

	// The parameter's text for the rows that name a file, and a name where the shape pairs one
	function text(list: Row[]): string {
		return list
			.filter((r) => r.file.trim() && (shape !== 'named' || r.name.trim()) && !apart(r))
			.map((r) => (shape === 'scaled' ? `${r.file.trim()}:${r.scale ?? 1}` : shape === 'named' ? `${r.name.trim()}=${r.file.trim()}` : r.file.trim()))
			.join(',');
	}

	// The second parameter's text, every row of a paired list scaled away from one
	function scaledText(list: Row[]): string {
		return list
			.filter((r) => r.file.trim() && apart(r))
			.map((r) => `${r.file.trim()}:${r.scale}`)
			.join(',');
	}

	// Takes values set from outside, a discarded form or another slot's params
	$effect(() => {
		const incoming = value;
		const incomingScaled = scaled ?? '';
		untrack(() => {
			if (incoming !== text(rows) || incomingScaled !== scaledText(rows)) rows = [...parse(incoming, shape), ...parse(incomingScaled, 'scaled')];
		});
	});

	function commit() {
		const next = text(rows);
		const nextScaled = scaledText(rows);
		if (next === value && nextScaled === (scaled ?? '')) return;
		value = next;
		if (paired) scaled = nextScaled;
		onchange?.(next, nextScaled);
	}

	function add() {
		rows = [...rows, { file: '', scale: 1, name: '' }];
	}

	function remove(i: number) {
		rows = rows.filter((_, j) => j !== i);
		commit();
	}
</script>

<div class="field">
	<FieldLabel {label} {info} />
	{#each rows as row, i (i)}
		<div class="row">
			{#if shape === 'named'}
				<span class="name"><NeoInput bind:value={row.name} {disabled} elevation={-1} floating={false} aria-label="Name" oninput={commit} /></span>
			{/if}
			<span class="file"><Path bind:value={row.file} {disabled} onchange={commit} /></span>
			{#if scales}
				<span class="scale"><NumberField bind:value={row.scale} step={0.05} unit="strength" plain aria="Strength" {disabled} onchange={commit} /></span>
			{/if}
			<NeoButton text aria-label="Remove" title="Remove" onclick={() => remove(i)} {disabled}>
				{#snippet icon()}<XIcon size={16} />{/snippet}
			</NeoButton>
		</div>
	{/each}
	<span class="add">
		<NeoButton text onclick={add} {disabled}>
			{#snippet icon()}<PlusIcon size={16} />{/snippet}
			Add {noun}
		</NeoButton>
	</span>
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.row {
		display: flex;
		gap: 8px;
		align-items: center;
		min-width: 0;
	}
	.file {
		flex: 1 1 0;
		min-width: 0;
	}
	.name {
		flex: 0 1 12ch;
		min-width: 8ch;
	}
	.scale {
		flex: 0 0 11rem;
	}
	/* The button sits at the list's left edge, its text on the line of the labels above */
	.add {
		display: flex;
		margin-left: -0.5rem;
	}
</style>
