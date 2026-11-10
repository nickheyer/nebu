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

	// One runtime parameter as the control its type asks for, empty ones showing the value they resolve to
	let { param, values = $bindable({}), state, resolved }: { param: Param; values?: Record<string, string>; state?: ParamState; resolved?: string } = $props();

	const p = $derived(param);
	const label = $derived(p.label || p.name);
	const info = $derived(p.description || undefined);
	const min = $derived(state?.min ?? p.min);
	const max = $derived(state?.max ?? p.max);
	const step = $derived(state?.step || p.step || (p.type === ParamType.FLOAT ? 0.01 : 1));
	const numeric = $derived(p.type === ParamType.INT || p.type === ParamType.FLOAT);
	// A zero max leaves a number unbounded, so only a positive max draws a slider
	const bounded = $derived(numeric && max > 0 && max > min);
	const current = $derived(values[p.name] ?? '');
	// The planner's value when it has one, else the default, a negative default standing for no value
	const standing = $derived(resolved || (numeric && Number(p.default) < 0 ? '' : p.default) || undefined);
	const fallback = $derived(standing !== undefined && Number.isFinite(Number(standing)) ? Number(standing) : undefined);

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

<div class="param">
	{#if p.choices.length}
		<Select value={current || undefined} options={p.choices.map((c) => ({ value: c, label: c }))} {label} {info} placeholder={standing} clearable onchange={set} />
	{:else if p.picks}
		<Select value={current || undefined} options={picks} {label} {info} placeholder={picks.find((o) => o.value === standing)?.label} clearable search onchange={set} />
	{:else if p.type === ParamType.BOOL}
		<Switch checked={(current || standing) === 'true'} {label} {info} top onchange={(on) => set(on ? 'true' : 'false')} />
	{:else if p.type === ParamType.PATH}
		<Path value={current} {label} {info} filter={p.directory ? 'directories' : 'any'} placeholder={standing} onchange={set} />
	{:else if bounded}
		<Slider value={current === '' ? undefined : Number(current)} min={Math.max(min, 0)} {max} {step} unit={p.unit || undefined} {label} {info} {fallback} optional onchange={set} />
	{:else if numeric}
		<NumberField value={current === '' ? undefined : Number(current)} min={Math.max(min, 0) || undefined} {step} unit={p.unit || undefined} {label} {info} {fallback} optional onchange={set} onclear={() => set(undefined)} />
	{:else}
		<Text value={current} {label} {info} placeholder={standing} onchange={set} />
	{/if}
</div>

<style>
	.param {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
	}
</style>
