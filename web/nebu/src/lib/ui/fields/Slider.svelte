<script lang="ts">
	import { untrack } from 'svelte';
	import { watch } from '@dvcol/svelte-utils/watch';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoInput, NeoRange } from '#lib/neo/inputs/index.js';
	import Tip from '#lib/ui/Tip.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { fieldId, OffIcon, type Off } from '#lib/ui/fields/types.js';

	let {
		value = $bindable(0),
		min = 0,
		max = 100,
		step = 1,
		unit,
		label,
		info,
		disabled = false,
		message,
		blank = false,
		fallback,
		// The words an empty box shows when no fallback number stands in
		placeholder,
		// The toggle emptying the field, blank then meaning what its label says
		off,
		onchange,
		onclear
	}: { value?: number; min?: number; max?: number; step?: number; unit?: string; label?: string; info?: string; disabled?: boolean; message?: string; blank?: boolean; fallback?: number; placeholder?: string; off?: Off; onchange?: (n: number) => void; onclear?: () => void } = $props();

	const id = fieldId();
	let typed = $state(untrack(() => (blank ? '' : String(value))));
	// The knob rests on the fallback while the value is unset
	let knob = $state(untrack(() => (blank && fallback !== undefined ? fallback : value)));
	// An empty field without a number to rest on shows its rail alone, the knob gone until a value lands
	const unset = $derived(blank && fallback === undefined);

	watch(
		() => {
			onchange?.(value);
		},
		() => value,
		{ skip: 1 }
	);

	$effect(() => {
		typed = blank ? '' : String(value);
		knob = blank && fallback !== undefined ? fallback : value;
	});

	watch(
		() => {
			if (knob !== (blank && fallback !== undefined ? fallback : value)) value = knob;
		},
		() => knob,
		{ skip: 1 }
	);

	function commit() {
		if (typed === '' && blank) return;
		const n = Number(typed);
		if (typed === '' || Number.isNaN(n)) {
			typed = blank ? '' : String(value);
			return;
		}
		value = Math.min(max, Math.max(min, n));
		typed = String(value);
	}

	// Clears the field, or sets an empty one to its fallback
	function toggle() {
		if (disabled) return;
		if (blank) value = fallback ?? min;
		else onclear?.();
	}
</script>

{#snippet after()}
	<span class="controls">
		{#if unit && (!blank || fallback !== undefined)}<span class="unit">{unit}</span>{/if}
		{#if off}
			<Tip text={off.label} tag="span">
				<NeoButton text toggle checked={blank} color={blank ? 'primary' : undefined} aria-label={off.label} aria-pressed={blank} {disabled} onclick={toggle}>{#snippet icon()}<OffIcon size={16} />{/snippet}</NeoButton>
			</Tip>
		{/if}
	</span>
{/snippet}

<div class="field">
	<FieldLabel {label} {info} forId={id} />
	<div class="controls" class:blank class:unset>
		<div class="range">
			<NeoRange bind:value={knob} {min} {max} {step} {disabled} elevation={-1} tooltips={false} width="100%" />
		</div>
		<div class="box" class:toggled={off !== undefined}>
			<NeoInput type="text" inputmode="decimal" {id} bind:value={typed} {disabled} placeholder={fallback !== undefined ? String(fallback) : blank ? (placeholder ?? off?.label ?? 'Default') : undefined} elevation={-1} floating={false} width="100%" after={unit || off ? after : undefined} afterProps={{ class: 'stepper-after' }} onchange={commit} onblur={commit} aria-label={label} />
		</div>
	</div>
	{#if message}<span class="secondary">{message}</span>{/if}
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.controls {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
	}
	.range {
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
		transition: opacity var(--nebu-normal) var(--nebu-ease);
	}
	.range :global(.neo-range-container) {
		min-width: 0;
	}
	.blank .range {
		opacity: 0.55;
	}
	.unset .range :global(.neo-range-handle) {
		opacity: 0;
	}
	.unset .range :global(.neo-range-handle-before) {
		background-color: transparent;
	}
	/* One box width for every slider, so the sliders of a column end on one line, wider when a toggle shares the box */
	.box {
		display: flex;
		flex: 0 0 9rem;
		min-width: 0;
	}
	.box.toggled {
		flex-basis: 11rem;
	}
	.box :global(.neo-input-group) {
		flex: 1 1 auto;
		min-width: 0;
	}
	.box :global(input) {
		text-align: right;
	}
	span.controls {
		display: inline-flex;
		gap: 2px;
	}
	span.controls :global(.neo-button) {
		--neo-btn-min-width: 2rem;
		--neo-btn-padding: 0.375rem 0.5rem;
		--neo-btn-padding-empty: 0.375rem;
	}
	.unit {
		padding: 0 8px 0 4px;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
</style>
