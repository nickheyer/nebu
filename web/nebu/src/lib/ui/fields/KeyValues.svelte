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
		// The word on the button that adds a row
		addLabel = 'Entry',
		secret = false,
		fixedKeys = false,
		disabled = false
	}: { value?: Record<string, string>; label?: string; info?: string; keyLabel?: string; valueLabel?: string; addLabel?: string; secret?: boolean; fixedKeys?: boolean; disabled?: boolean } = $props();

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
	{#if rows.length}
		<div class="heads" aria-hidden="true">
			<span class="head">{keyLabel}</span>
			<span class="head">{valueLabel}</span>
			{#if !fixedKeys}<span class="headgap"></span>{/if}
		</div>
	{/if}
	{#each rows as row, i (i)}
		<div class="pair">
			{#if fixedKeys}
				<span class="key">{row.k}</span>
			{:else}
				<NeoInput bind:value={row.k} {disabled} elevation={-1} floating={false} aria-label={keyLabel} />
			{/if}
			{#if secret}
				<NeoPassword bind:value={row.v} {disabled} elevation={-1} floating={false} aria-label={valueLabel} />
			{:else}
				<NeoInput bind:value={row.v} {disabled} elevation={-1} floating={false} aria-label={valueLabel} />
			{/if}
			{#if !fixedKeys}
				<NeoButton text aria-label="Remove" title="Remove" onclick={() => remove(i)} {disabled}>
					{#snippet icon()}<XIcon size={16} />{/snippet}
				</NeoButton>
			{/if}
		</div>
	{/each}
	{#if !fixedKeys}
		<span class="add">
			<NeoButton text onclick={add} {disabled}>
				{#snippet icon()}<PlusIcon size={16} />{/snippet}
				{addLabel}
			</NeoButton>
		</span>
	{/if}
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.heads {
		display: flex;
		gap: 8px;
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-muted);
	}
	.head {
		flex: 1 1 0;
		min-width: 0;
	}
	.headgap {
		flex: 0 0 36px;
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
	/* The button that adds a row sits where the row will appear */
	.add {
		display: inline-flex;
		align-self: flex-start;
	}
	/* A fixed key is a label, read but never typed in */
	.key {
		flex: 1 1 0;
		min-width: 0;
		padding-left: var(--nebu-space-1);
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		overflow-wrap: anywhere;
	}
</style>
