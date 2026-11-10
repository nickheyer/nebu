<script lang="ts">
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import { NeoPill } from '#lib/neo/pill/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';

	let {
		value = $bindable([]),
		label,
		info,
		placeholder,
		disabled = false,
		numeric = false,
		max,
		// A toolbar field, its label standing as the placeholder and the chips kept inside the box
		compact = false,
		onchange
	}: { value?: string[]; label?: string; info?: string; placeholder?: string; disabled?: boolean; numeric?: boolean; max?: number; compact?: boolean; onchange?: (v: string[]) => void } = $props();

	const id = fieldId();
	let typed = $state('');

	function add() {
		const v = typed.trim();
		if (!v) return;
		if (numeric && Number.isNaN(Number(v))) return;
		if (max && value.length >= max) return;
		if (!value.includes(v)) {
			value = [...value, v];
			onchange?.(value);
		}
		typed = '';
	}

	function remove(v: string) {
		value = value.filter((x) => x !== v);
		onchange?.(value);
	}

	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' || e.key === ',') {
			e.preventDefault();
			add();
		} else if (e.key === 'Backspace' && typed === '' && value.length) {
			value = value.slice(0, -1);
			onchange?.(value);
		}
	}
</script>

{#snippet pills()}
	{#each value as v (v)}
		<NeoPill size="small" close label={v} onClose={() => remove(v)} />
	{/each}
{/snippet}

<div class="field" class:compact>
	{#if !compact}
		<FieldLabel {label} {info} forId={id} />
		<div class="chips">{@render pills()}</div>
	{/if}
	<NeoInput bind:value={typed} {id} {disabled} placeholder={placeholder ?? (compact ? label : undefined)} elevation={-1} floating={false} width="100%" inputmode={numeric ? 'numeric' : undefined} {onkeydown} onblur={add} aria-label={label}>
		{#snippet before()}
			{#if compact && value.length}<span class="chips inline">{@render pills()}</span>{:else}<span class="plus"><PlusIcon size={16} /></span>{/if}
		{/snippet}
	</NeoInput>
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.field :global(.neo-input-group) {
		min-width: 0;
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}
	.chips:empty {
		display: none;
	}
	.inline {
		flex-wrap: nowrap;
	}
	.compact :global(.neo-input-group) {
		margin-top: 0;
	}
	.plus {
		display: inline-flex;
		color: var(--nebu-text-faint);
	}
</style>
