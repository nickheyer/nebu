<script lang="ts">
	import { ParamType, type Param } from '#lib/proto/nebu/v1/runtime_pb.js';
	import type { ParamState } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { live } from '#lib/live/events.svelte.js';
	import Tip from '#lib/ui/Tip.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import FunctionIcon from 'phosphor-svelte/lib/FunctionIcon';

	let { param, values = $bindable({}), state }: { param: Param; values?: Record<string, string>; state?: ParamState } = $props();

	const p = $derived(param);
	const label = $derived(p.label || p.name);
	const min = $derived(state?.min ?? p.min);
	const max = $derived(state?.max ?? p.max);
	const step = $derived(state?.step || p.step || (p.type === ParamType.FLOAT ? 0.01 : 1));
	const bounded = $derived((p.type === ParamType.INT || p.type === ParamType.FLOAT) && max > min);
	const current = $derived(values[p.name] ?? '');
	// A solved param resolves to no single value, so its empty box carries the planner's mark and no words
	const placeholder = $derived(p.solved ? undefined : p.default || undefined);
	const fallback = $derived(p.solved || !p.default ? undefined : Number(p.default));
	const planned = $derived(p.solved && current === '');

	// Stored parts whose architecture the param picks, any stored model when it picks a model
	const picks = $derived.by(() => {
		if (!p.picks) return [];
		return [...live.models.values()]
			.filter((m) => p.picks === 'model' || p.picks === 'checkpoint' || m.descriptor?.architecture === p.picks)
			.map((m) => ({ value: m.path, label: `${m.repo}${m.group ? ` ${m.group}` : ''}`, description: m.formatId }));
	});

	function set(v: string | number | undefined) {
		if (v === undefined || v === '') delete values[p.name];
		else values[p.name] = String(v);
	}
</script>

{#snippet planner()}
	<Tip text={p.rule || 'planner'} tag="span"><span class="solved"><FunctionIcon size={16} /></span></Tip>
{/snippet}

<div class="param">
	{#if p.choices.length}
		<Select value={current || undefined} options={p.choices.map((c) => ({ value: c, label: c }))} {label} {placeholder} clearable mark={planned ? planner : undefined} onchange={set} />
	{:else if p.picks}
		<Select value={current || undefined} options={picks} {label} clearable search mark={planned ? planner : undefined} onchange={set} />
	{:else if p.type === ParamType.BOOL}
		<Switch checked={(current || p.default) === 'true'} {label} onchange={(on) => set(on ? 'true' : 'false')} />
	{:else if p.type === ParamType.PATH}
		<Path value={current} {label} filter={p.directory ? 'directories' : 'any'} {placeholder} mark={planned ? planner : undefined} onchange={set} />
	{:else if bounded}
		<Slider value={Number(current) || fallback || min} {min} {max} {step} unit={p.unit || undefined} {label} blank={current === ''} {fallback} mark={p.solved ? planner : undefined} onchange={set} />
	{:else if p.type === ParamType.INT || p.type === ParamType.FLOAT}
		<NumberField value={current === '' ? 0 : Number(current)} min={min || undefined} {step} unit={p.unit || undefined} {label} emptyZero={current === ''} {placeholder} mark={planned ? planner : undefined} onchange={set} />
	{:else}
		<Text value={current} {label} {placeholder} onchange={set} />
	{/if}
</div>

<style>
	.param {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
	}
	.solved {
		display: inline-flex;
		color: var(--neo-color-primary);
	}
</style>
