<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoInput, NeoPassword } from '#lib/neo/inputs/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import XIcon from 'phosphor-svelte/lib/XIcon';

	let {
		value = $bindable({}),
		label,
		info,
		keyLabel = 'Key',
		valueLabel = 'Value',
		secret = false,
		fixedKeys = false,
		disabled = false,
		secretsSet = false
	}: { value?: Record<string, string>; label?: string; info?: string; keyLabel?: string; valueLabel?: string; secret?: boolean; fixedKeys?: boolean; disabled?: boolean; secretsSet?: boolean } = $props();

	let rows = $state<{ k: string; v: string }[]>(Object.entries(value).map(([k, v]) => ({ k, v })));

	$effect(() => {
		const next: Record<string, string> = {};
		for (const r of rows) if (r.k.trim()) next[r.k.trim()] = r.v;
		value = next;
	});

	function add() {
		rows = [...rows, { k: '', v: '' }];
	}

	function remove(i: number) {
		rows = rows.filter((_, j) => j !== i);
	}
</script>

<div class="field">
	<FieldLabel {label} {info} />
	{#each rows as row, i (i)}
		<div class="pair">
			<NeoInput bind:value={row.k} placeholder={keyLabel} readonly={fixedKeys} {disabled} elevation={-1} floating={false} aria-label={keyLabel} />
			{#if secret}
				<NeoPassword bind:value={row.v} placeholder={secretsSet && !row.v ? 'set' : valueLabel} {disabled} elevation={-1} floating={false} aria-label={valueLabel} />
			{:else}
				<NeoInput bind:value={row.v} placeholder={valueLabel} {disabled} elevation={-1} floating={false} aria-label={valueLabel} />
			{/if}
			{#if !fixedKeys}
				<NeoButton text aria-label="Remove" onclick={() => remove(i)} {disabled}>
					{#snippet icon()}<XIcon size={16} />{/snippet}
				</NeoButton>
			{/if}
		</div>
	{/each}
	{#if !fixedKeys}
		<div>
			<NeoButton text onclick={add} {disabled}>
				{#snippet icon()}<PlusIcon size={16} />{/snippet}
				Add
			</NeoButton>
		</div>
	{/if}
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.pair {
		display: flex;
		gap: 8px;
		align-items: center;
	}
	.pair :global(.neo-input-group) {
		flex: 1 1 0;
		min-width: 0;
	}
</style>
