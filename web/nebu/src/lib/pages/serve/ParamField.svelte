<script lang="ts">
	import { ParamType, type Param } from '#lib/proto/nebu/v1/runtime_pb.js';
	import type { ParamState } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { live } from '#lib/live/events.svelte.js';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';

	let {
		param,
		values = $bindable({}),
		auto = $bindable({}),
		state
	}: { param: Param; values?: Record<string, string>; auto?: Record<string, boolean>; state?: ParamState } = $props();

	const p = $derived(param);
	const label = $derived(p.label || p.name);
	const automatic = $derived(p.solved && (auto[p.name] ?? true));
	const min = $derived(state?.min ?? p.min);
	const max = $derived(state?.max ?? p.max);
	const step = $derived(state?.step || p.step || (p.type === ParamType.FLOAT ? 0.01 : 1));
	const bounded = $derived((p.type === ParamType.INT || p.type === ParamType.FLOAT) && max > min);
	const current = $derived(values[p.name] ?? '');
	const info = $derived(p.description || undefined);

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

	function toggleAuto(on: boolean) {
		auto[p.name] = on;
		if (on) delete values[p.name];
	}
</script>

<div class="param">
	{#if p.solved}
		<Switch checked={automatic} label="{label} auto" info={p.rule || 'chosen by the planner at launch'} onchange={toggleAuto} />
	{/if}
	{#if !automatic}
		{#if p.choices.length}
			<Select value={current || undefined} options={p.choices.map((c) => ({ value: c, label: c }))} {label} {info} clearable onchange={set} />
		{:else if p.picks}
			<Select value={current || undefined} options={picks} {label} {info} clearable search placeholder="Stored part" onchange={set} />
		{:else if p.type === ParamType.BOOL}
			<Switch checked={(current || p.default) === 'true'} {label} {info} onchange={(on) => set(on ? 'true' : 'false')} />
		{:else if p.type === ParamType.PATH}
			<Path value={current} {label} {info} filter={p.directory ? 'directories' : 'any'} placeholder={p.default} onchange={set} />
		{:else if bounded}
			<Slider value={Number(current || p.default) || min} {min} {max} {step} unit={p.unit || undefined} {label} {info} onchange={set} />
		{:else if p.type === ParamType.INT || p.type === ParamType.FLOAT}
			<NumberField value={current === '' ? Number(p.default) || 0 : Number(current)} min={min || undefined} {step} unit={p.unit || undefined} {label} {info} placeholder={p.default} onchange={set} />
		{:else}
			<Text value={current} {label} {info} placeholder={p.default} onchange={set} />
		{/if}
	{/if}
</div>

<style>
	.param {
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
	}
</style>
