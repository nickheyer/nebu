<script lang="ts" generics="V extends string | number | boolean">
	import { NeoButton, NeoButtonGroup } from '#lib/neo/buttons/index.js';
	import Tip from '#lib/ui/Tip.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import type { Option } from '#lib/ui/fields/types.js';

	// One choice as a row of buttons, or as a select where the row is wider than the space it has
	let {
		value = $bindable(),
		options,
		label,
		info,
		message,
		disabled = false,
		clearable = false,
		// The name a group without a label goes by
		aria,
		onchange
	}: { value?: V; options: Option<V>[]; label?: string; info?: string; message?: string; disabled?: boolean; clearable?: boolean; aria?: string; onchange?: (v: V) => void } = $props();

	let room = $state(0);
	let group = $state<HTMLElement>();
	// Pixels the buttons take on one line, read while they are drawn
	let natural = $state(0);
	const fits = $derived(natural === 0 || room === 0 || natural <= room);
	const labels = $derived(options.map((o) => o.label).join('\n'));

	// Reads the row again whenever its labels change or the fonts arrive
	$effect.pre(() => {
		void labels;
		natural = 0;
	});
	$effect(() => {
		void labels;
		if (!group) return;
		const row = group;
		natural = span(row);
		void document.fonts.ready.then(() => {
			if (row.isConnected) natural = span(row);
		});
	});

	// Pixels the row takes with its border and margins, however much of it shows
	function span(row: HTMLElement): number {
		const style = getComputedStyle(row);
		return row.scrollWidth + row.offsetWidth - row.clientWidth + parseFloat(style.marginLeft) + parseFloat(style.marginRight);
	}

	// Pressing the chosen option again releases it when the group may stand empty
	function pick(v: V) {
		if (disabled) return;
		if (clearable && value === v) {
			value = undefined;
			return;
		}
		value = v;
		onchange?.(v);
	}

	// Takes the select's choice by the text of its value, an emptied select releasing the group
	function picked(key: string | undefined) {
		const o = options.find((x) => String(x.value) === key);
		if (o) pick(o.value);
		else if (clearable) value = undefined;
	}
</script>

<div class="field" bind:clientWidth={room}>
	{#if fits}
		<FieldLabel {label} {info} />
		<NeoButtonGroup bind:ref={group} elevation={-1} nowrap role="radiogroup" aria-label={label ?? aria}>
			{#each options as o (String(o.value))}
				<Tip text={o.tip} tag="span">
					<NeoButton
						role="radio"
						aria-checked={value === o.value}
						checked={value === o.value}
						toggle
						disabled={disabled || o.disabled}
						color={value === o.value ? 'primary' : undefined}
						onclick={() => pick(o.value)}
					>
						{o.label}
					</NeoButton>
				</Tip>
			{/each}
		</NeoButtonGroup>
	{:else}
		<Select value={value === undefined ? undefined : String(value)} options={options.map((o) => ({ value: String(o.value), label: o.label, description: o.tip, disabled: o.disabled }))} label={label ?? aria} compact={label === undefined} {info} {disabled} {clearable} onchange={picked} />
	{/if}
	{#if message}<span class="secondary" aria-live="polite">{message}</span>{/if}
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.field :global(.neo-button-group .neo-button) {
		white-space: nowrap;
	}
</style>
