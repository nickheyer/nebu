<script lang="ts">
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import { NeoPill } from '#lib/neo/pill/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';

	let {
		value = $bindable([]),
		label,
		disabled = false,
		numeric = false,
		max,
		onchange
	}: { value?: string[]; label?: string; disabled?: boolean; numeric?: boolean; max?: number; onchange?: (v: string[]) => void } = $props();

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

<div class="field">
	<FieldLabel {label} />
	<div class="chips">
		{#each value as v (v)}
			<NeoPill size="small" close label={v} onClose={() => remove(v)} />
		{/each}
	</div>
	<NeoInput bind:value={typed} {disabled} elevation={-1} floating={false} width="100%" inputmode={numeric ? 'numeric' : undefined} {onkeydown} onblur={add} aria-label={label}>
		{#snippet before()}<span class="plus"><PlusIcon size={16} /></span>{/snippet}
	</NeoInput>
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
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
	.plus {
		display: inline-flex;
		color: var(--nebu-text-faint);
	}
</style>
