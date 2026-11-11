<script lang="ts">
	import { rpc } from '#lib/api/transport.js';
	import { bytes } from '#lib/format.js';
	import { ParamList, ParamType, type Param } from '#lib/proto/nebu/v1/runtime_pb.js';
	import type { ParamState } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { ModelSort } from '#lib/proto/nebu/v1/store_pb.js';
	import FileList, { type FileListShape } from '#lib/ui/fields/FileList.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Part from '#lib/ui/fields/Part.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import type { PickerPage } from '#lib/ui/fields/types.js';
	import { modelPhrase, partLabel, storeRef } from '#lib/pages/serve/serve.js';

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

	const shapes: Partial<Record<ParamList, FileListShape>> = { [ParamList.FILES]: 'files', [ParamList.SCALED]: 'scaled', [ParamList.NAMED]: 'named' };
	// A file the planner settled on for an empty box, the words auto and none naming no file
	const settled = $derived(resolved && resolved !== 'auto' && resolved !== 'none' ? partLabel(resolved) : undefined);

	// Every stored group, each as the reference a launch resolves to the file the param loads
	async function fetchParts(query: string, cursor: string, limit: number, signal: AbortSignal): Promise<PickerPage<string>> {
		const res = await rpc.store.listModels({ query, cursor, limit, sort: ModelSort.LAST_USED }, { signal });
		return { items: res.models.map((m) => ({ value: storeRef(m), label: modelPhrase(m), description: bytes(m.bytes) })), next: res.nextCursor };
	}

	// The param's own choices holding the typed words, then every stored group by its group name
	async function fetchNamed(query: string, cursor: string, limit: number, signal: AbortSignal): Promise<PickerPage<string>> {
		const res = await rpc.store.listModels({ query, cursor, limit, sort: ModelSort.LAST_USED }, { signal });
		const words = query.toLowerCase().split(/\s+/).filter(Boolean);
		const own = cursor ? [] : p.choices.filter((c) => c !== '' && words.every((w) => c.toLowerCase().includes(w))).map((c) => ({ value: c, label: c }));
		return { items: [...own, ...res.models.map((m) => ({ value: m.group, label: m.group, description: m.repo }))], next: res.nextCursor };
	}

	function set(v: string | number | undefined) {
		if (v === undefined || v === '') delete values[p.name];
		else values[p.name] = String(v);
	}
</script>

<div class="param">
	{#if p.picks && p.type === ParamType.PATH}
		<Part value={current} fetch={fetchParts} {label} {info} placeholder={settled} current={current ? partLabel(current) : undefined} filter={p.directory ? 'directories' : 'any'} onchange={set} />
	{:else if p.picks}
		<Picker value={current || undefined} fetch={fetchNamed} {label} {info} placeholder={standing} current={current || undefined} clearable onchange={set} />
	{:else if shapes[p.list]}
		<FileList value={current} shape={shapes[p.list]} {label} {info} onchange={set} />
	{:else if p.choices.length}
		<Select value={current || undefined} options={p.choices.map((c) => ({ value: c, label: c }))} {label} {info} placeholder={standing} clearable onchange={set} />
	{:else if p.type === ParamType.BOOL}
		<Switch checked={(current || standing) === 'true'} {label} {info} onchange={(on) => set(on ? 'true' : 'false')} />
	{:else if p.type === ParamType.PATH}
		<Path value={current} {label} {info} filter={p.directory ? 'directories' : 'any'} placeholder={standing} onchange={set} />
	{:else if bounded}
		<Slider value={current === '' ? undefined : Number(current)} {min} {max} {step} unit={p.unit || undefined} {label} {info} {fallback} optional onchange={set} />
	{:else if numeric}
		<NumberField value={current === '' ? undefined : Number(current)} min={min || undefined} {step} unit={p.unit || undefined} {label} {info} {fallback} optional onchange={set} onclear={() => set(undefined)} />
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
