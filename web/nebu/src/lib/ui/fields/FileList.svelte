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
		shape = 'files',
		label,
		info,
		disabled = false,
		onchange
	}: { value?: string; shape?: FileListShape; label?: string; info?: string; disabled?: boolean; onchange?: (v: string) => void } = $props();

	let rows = $state<Row[]>(untrack(() => parse(value)));

	// One row per comma separated item, the scale after its last colon and the name before its first equals sign
	function parse(text: string): Row[] {
		return text
			.split(',')
			.map((item) => item.trim())
			.filter(Boolean)
			.map((item) => {
				if (shape === 'scaled') {
					const at = item.lastIndexOf(':');
					const scale = at > 0 ? Number(item.slice(at + 1)) : NaN;
					return Number.isFinite(scale) ? { file: item.slice(0, at), scale, name: '' } : { file: item, scale: 1, name: '' };
				}
				if (shape === 'named') {
					const at = item.indexOf('=');
					return at > 0 ? { file: item.slice(at + 1), scale: 1, name: item.slice(0, at) } : { file: item, scale: 1, name: '' };
				}
				return { file: item, scale: 1, name: '' };
			});
	}

	// The parameter's text for the rows that name a file, and a name where the shape pairs one
	function text(list: Row[]): string {
		return list
			.filter((r) => r.file.trim() && (shape !== 'named' || r.name.trim()))
			.map((r) => (shape === 'scaled' ? `${r.file.trim()}:${r.scale ?? 1}` : shape === 'named' ? `${r.name.trim()}=${r.file.trim()}` : r.file.trim()))
			.join(',');
	}

	// Takes a value set from outside, a discarded form or another slot's params
	$effect(() => {
		const incoming = value;
		untrack(() => {
			if (incoming !== text(rows)) rows = parse(incoming);
		});
	});

	function commit() {
		const next = text(rows);
		if (next === value) return;
		value = next;
		onchange?.(next);
	}

	function add() {
		rows = [...rows, { file: '', scale: 1, name: '' }];
	}

	function remove(i: number) {
		rows = rows.filter((_, j) => j !== i);
		commit();
	}
</script>

{#snippet addButton()}
	<NeoButton text onclick={add} {disabled} aria-label="Add {label ?? 'file'}" title="Add">
		{#snippet icon()}<PlusIcon size={16} />{/snippet}
	</NeoButton>
{/snippet}

<div class="field">
	<FieldLabel {label} {info} end={addButton} />
	{#each rows as row, i (i)}
		<div class="row">
			{#if shape === 'named'}
				<span class="name"><NeoInput bind:value={row.name} {disabled} elevation={-1} floating={false} aria-label="Name" oninput={commit} /></span>
			{/if}
			<span class="file"><Path bind:value={row.file} {disabled} onchange={commit} /></span>
			{#if shape === 'scaled'}
				<span class="scale"><NumberField bind:value={row.scale} step={0.05} {disabled} onchange={commit} /></span>
			{/if}
			<NeoButton text aria-label="Remove" title="Remove" onclick={() => remove(i)} {disabled}>
				{#snippet icon()}<XIcon size={16} />{/snippet}
			</NeoButton>
		</div>
	{/each}
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
		flex: 0 0 9ch;
	}
</style>
