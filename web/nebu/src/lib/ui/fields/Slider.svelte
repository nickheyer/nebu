<script lang="ts">
	import type { Snippet } from 'svelte';
	import { untrack } from 'svelte';
	import { watch } from '@dvcol/svelte-utils/watch';
	import { NeoInput, NeoRange } from '#lib/neo/inputs/index.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';

	let {
		value = $bindable(0),
		min = 0,
		max = 100,
		step = 1,
		unit,
		label,
		disabled = false,
		message,
		blank = false,
		fallback,
		mark,
		onchange
	}: { value?: number; min?: number; max?: number; step?: number; unit?: string; label?: string; disabled?: boolean; message?: string; blank?: boolean; fallback?: number; mark?: Snippet; onchange?: (n: number) => void } = $props();

	let typed = $state(untrack(() => (blank ? '' : String(value))));
	// The knob rests on the fallback while the value is unset
	let knob = $state(untrack(() => (blank && fallback !== undefined ? fallback : value)));

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
</script>

<div class="field">
	<FieldLabel {label} />
	<div class="controls" class:blank>
		<div class="range">
			<NeoRange bind:value={knob} {min} {max} {step} {disabled} elevation={-1} tooltips={false} width="100%" />
		</div>
		<div class="box">
			<NeoInput type="text" inputmode="decimal" bind:value={typed} {disabled} placeholder={fallback === undefined ? undefined : String(fallback)} elevation={-1} floating={false} width="100%" onchange={commit} onblur={commit} aria-label={label} />
		</div>
		{#if unit}<span class="unit secondary">{unit}</span>{/if}
		{#if mark && blank}<span class="mark">{@render mark()}</span>{/if}
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
	.box {
		display: flex;
		flex: 0 0 9ch;
		min-width: 0;
	}
	.box :global(.neo-input-group) {
		min-width: 0;
	}
	.box :global(input) {
		text-align: right;
	}
	.unit {
		flex: 0 0 auto;
	}
	.mark {
		display: inline-flex;
		flex: 0 0 auto;
		color: var(--nebu-text-muted);
	}
</style>
